package modelgovernance

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/worksflow/builder/backend/internal/templateauthority"
)

type governanceTestKeys struct {
	policy      GovernanceTrustPolicy
	revocations GovernanceRevocationAuthority
	private     map[string]ed25519.PrivateKey
}

type governanceFixture struct {
	now            time.Time
	materials      GovernanceMaterials
	receiptDigest  string
	profile        ModelProfile
	profileHash    string
	corpus         FrozenCorpus
	route          ProviderRouteAuthority
	conformance    ConformanceArtifact
	shadow         ShadowArtifact
	approval       ApprovalArtifact
	activation     ActivationArtifact
	receipt        ModelGovernanceReceipt
	conformanceRef GovernanceArtifactRef
	shadowRef      GovernanceArtifactRef
	approvalRef    GovernanceArtifactRef
	activationRef  GovernanceArtifactRef
}

func TestGovernanceTrustPolicyCanonicalHashAndSubstitutionFence(t *testing.T) {
	now := governanceTestNow()
	keys := newGovernanceTestKeys(t, now, "", time.Time{})
	encoded, err := CanonicalGovernanceTrustPolicyJSON(keys.policy)
	if err != nil {
		t.Fatalf("canonical trust policy: %v", err)
	}
	if sha256Digest(encoded) != keys.policy.PolicyHash {
		t.Fatal("trust policy hash does not cover canonical bytes")
	}
	parsed, err := ParseGovernanceTrustPolicy(encoded, keys.policy.PolicyHash)
	if err != nil {
		t.Fatalf("parse trust policy: %v", err)
	}
	second, err := CanonicalGovernanceTrustPolicyJSON(parsed)
	if err != nil || !bytes.Equal(second, encoded) {
		t.Fatalf("trust policy round trip drifted: %v", err)
	}

	substituted := cloneGovernanceTrustPolicy(keys.policy)
	signer := substituted.Signers[RoleProfileApprover]
	signer.Identity = "substituted-approval-identity"
	substituted.Signers[RoleProfileApprover] = signer
	if err := ValidateGovernanceTrustPolicy(substituted); err == nil || !strings.Contains(err.Error(), "hash") {
		t.Fatalf("same PolicyHash with substituted trust was accepted: %v", err)
	}

	pretty := append([]byte(" "), encoded...)
	if _, err := ParseGovernanceTrustPolicy(pretty, sha256Digest(pretty)); err == nil {
		t.Fatal("non-canonical trust policy was accepted")
	}
}

func TestGovernanceRevocationAuthorityCanonicalHashAndSubstitutionFence(t *testing.T) {
	now := governanceTestNow()
	authority := newGovernanceTestKeys(t, now, "", time.Time{}).revocations
	encoded, err := CanonicalGovernanceRevocationAuthorityJSON(authority)
	if err != nil {
		t.Fatalf("canonical revocation authority: %v", err)
	}
	if sha256Digest(encoded) != authority.AuthorityHash {
		t.Fatal("revocation authority hash does not cover canonical bytes")
	}
	parsed, err := ParseGovernanceRevocationAuthority(encoded, authority.AuthorityHash)
	if err != nil {
		t.Fatalf("parse revocation authority: %v", err)
	}
	second, err := CanonicalGovernanceRevocationAuthorityJSON(parsed)
	if err != nil || !bytes.Equal(second, encoded) {
		t.Fatalf("revocation authority round trip drifted: %v", err)
	}
	substituted := cloneGovernanceRevocationAuthority(authority)
	substituted.Epoch++
	if err := ValidateGovernanceRevocationAuthority(substituted, now); err == nil || !strings.Contains(err.Error(), "hash") {
		t.Fatalf("same AuthorityHash with substituted epoch was accepted: %v", err)
	}
	pretty := append([]byte(" "), encoded...)
	if _, err := ParseGovernanceRevocationAuthority(pretty, sha256Digest(pretty)); err == nil {
		t.Fatal("non-canonical revocation authority was accepted")
	}
}

func TestGovernanceVerifierClosesExactIndependentAuthority(t *testing.T) {
	now := governanceTestNow()
	keys := newGovernanceTestKeys(t, now, "", time.Time{})
	profile := validModelProfile()
	fixture := buildGovernanceFixture(t, now, keys, profile, 2, testDigest("fence-one"), BaselineBinding{
		ActivationFence: testDigest("fence-one"), Generation: 1, MetricsHash: testDigest("baseline-metrics"),
		Profile:       CorpusProfileBinding{ID: "22222222-2222-4222-8222-222222222222", ContentHash: testDigest("baseline-profile"), Workload: profile.Workload},
		ReceiptDigest: testDigest("baseline-receipt"),
	}, RoleProfileApprover)
	verified, err := NewGovernanceVerifier().Verify(fixture.materials, fixture.receiptDigest, keys.policy, keys.revocations, now)
	if err != nil {
		t.Fatalf("verify closed governance authority: %v", err)
	}
	if verified.Profile.ID != profile.ID || verified.ReceiptEnvelopeDigest != fixture.receiptDigest ||
		verified.SignerIdentities[RoleProfileApprover] == verified.SignerIdentities[RoleActivationApprover] {
		t.Fatal("verified authority lost exact profile, receipt, or independent role identity")
	}
	if _, err := NewGovernanceVerifier().Verify(fixture.materials, fixture.receiptDigest, keys.policy, keys.revocations, now.Add(13*time.Hour)); err == nil {
		t.Fatal("expired governance receipt was accepted")
	}

	wrongRole := buildGovernanceFixture(t, now, keys, profile, 2, testDigest("fence-one"), fixture.shadow.Baseline, RoleActivationApprover)
	if _, err := NewGovernanceVerifier().Verify(wrongRole.materials, wrongRole.receiptDigest, keys.policy, keys.revocations, now); err == nil || !strings.Contains(err.Error(), RoleProfileApprover) {
		t.Fatalf("activation approver was accepted as profile approval signer: %v", err)
	}

	drifted := fixture.materials
	drifted.ModelProfileJSON = append([]byte(nil), fixture.materials.ModelProfileJSON...)
	drifted.ModelProfileJSON[len(drifted.ModelProfileJSON)-1] = ' '
	if _, err := NewGovernanceVerifier().Verify(drifted, fixture.receiptDigest, keys.policy, keys.revocations, now); err == nil {
		t.Fatal("profile byte drift was accepted")
	}
}

func TestGovernanceVerifierFailsClosedOnKeyRevocationAndTime(t *testing.T) {
	now := governanceTestNow()
	revokedAt := now.Add(time.Minute)
	keys := newGovernanceTestKeys(t, now, RoleReceiptIssuer, revokedAt)
	profile := validModelProfile()
	fixture := buildGovernanceFixture(t, now, keys, profile, 2, testDigest("fence-one"), BaselineBinding{
		ActivationFence: testDigest("fence-one"), Generation: 1, MetricsHash: testDigest("baseline-metrics"),
		Profile:       CorpusProfileBinding{ID: "22222222-2222-4222-8222-222222222222", ContentHash: testDigest("baseline-profile"), Workload: profile.Workload},
		ReceiptDigest: testDigest("baseline-receipt"),
	}, RoleProfileApprover)
	verifier := NewGovernanceVerifier()
	if _, err := verifier.Verify(fixture.materials, fixture.receiptDigest, keys.policy, keys.revocations, now); err != nil {
		t.Fatalf("pre-revocation authority was rejected: %v", err)
	}
	if _, err := verifier.Verify(fixture.materials, fixture.receiptDigest, keys.policy, keys.revocations, revokedAt); err == nil {
		t.Fatal("receipt issuer revocation was ignored")
	}
}

func TestGovernanceVerifierFailsClosedOnDigestRevocation(t *testing.T) {
	now := governanceTestNow()
	keys := newGovernanceTestKeys(t, now, "", time.Time{})
	profile := validModelProfile()
	keys.revocations.DigestRevocations = []GovernanceRevocation{{
		Digest: profile.Runner.ImmutableDigest, ReasonHash: testDigest("runner-revocation-reason"), RevokedAt: now.Add(time.Minute),
	}}
	keys.revocations.AuthorityHash = ""
	revocationHash, err := GovernanceRevocationAuthorityHash(keys.revocations)
	if err != nil {
		t.Fatal(err)
	}
	keys.revocations.AuthorityHash = revocationHash
	fixture := buildGovernanceFixture(t, now, keys, profile, 2, testDigest("fence-one"), BaselineBinding{
		ActivationFence: testDigest("fence-one"), Generation: 1, MetricsHash: testDigest("baseline-metrics"),
		Profile:       CorpusProfileBinding{ID: "22222222-2222-4222-8222-222222222222", ContentHash: testDigest("baseline-profile"), Workload: profile.Workload},
		ReceiptDigest: testDigest("baseline-receipt"),
	}, RoleProfileApprover)
	verifier := NewGovernanceVerifier()
	if _, err := verifier.Verify(fixture.materials, fixture.receiptDigest, keys.policy, keys.revocations, now); err != nil {
		t.Fatalf("pre-revocation authority was rejected: %v", err)
	}
	if _, err := verifier.Verify(fixture.materials, fixture.receiptDigest, keys.policy, keys.revocations, now.Add(time.Minute)); err == nil || !errors.Is(err, ErrGovernanceUntrusted) {
		t.Fatalf("runner digest revocation was ignored: %v", err)
	}
}

func TestGovernanceVerifierAppliesNestedAuthorityDigestRevocations(t *testing.T) {
	now := governanceTestNow()
	keys := newGovernanceTestKeys(t, now, "", time.Time{})
	profile := validModelProfile()
	fixture := buildGovernanceFixture(t, now, keys, profile, 2, testDigest("fence-one"), BaselineBinding{
		ActivationFence: testDigest("fence-one"), Generation: 1, MetricsHash: testDigest("baseline-metrics"),
		Profile:       CorpusProfileBinding{ID: "22222222-2222-4222-8222-222222222222", ContentHash: testDigest("baseline-profile"), Workload: profile.Workload},
		ReceiptDigest: testDigest("baseline-receipt"),
	}, RoleProfileApprover)
	targets := map[string]string{
		"execution prompt":  fixture.profile.Execution.PromptHash,
		"route TLS":         fixture.route.TLSIdentityHash,
		"oracle key policy": fixture.corpus.Cases[0].HiddenOracle.KeyPolicyHash,
	}
	for name, digest := range targets {
		t.Run(name, func(t *testing.T) {
			authority := cloneGovernanceRevocationAuthority(keys.revocations)
			authority.DigestRevocations = []GovernanceRevocation{{
				Digest: digest, ReasonHash: testDigest("nested-revocation-" + name), RevokedAt: now,
			}}
			authority.AuthorityHash = ""
			authorityHash, err := GovernanceRevocationAuthorityHash(authority)
			if err != nil {
				t.Fatal(err)
			}
			authority.AuthorityHash = authorityHash
			if _, err := NewGovernanceVerifier().Verify(fixture.materials, fixture.receiptDigest, keys.policy, authority, now); err == nil || !errors.Is(err, ErrGovernanceUntrusted) {
				t.Fatalf("nested authority digest revocation was ignored: %v", err)
			}
		})
	}
}

func TestGovernanceTimeChainRejectsPrematureAndEqualBoundaries(t *testing.T) {
	now := governanceTestNow()
	keys := newGovernanceTestKeys(t, now, "", time.Time{})
	profile := validModelProfile()
	fixture := buildGovernanceFixture(t, now, keys, profile, 2, testDigest("fence-one"), BaselineBinding{
		ActivationFence: testDigest("fence-one"), Generation: 1, MetricsHash: testDigest("baseline-metrics"),
		Profile:       CorpusProfileBinding{ID: "22222222-2222-4222-8222-222222222222", ContentHash: testDigest("baseline-profile"), Workload: profile.Workload},
		ReceiptDigest: testDigest("baseline-receipt"),
	}, RoleProfileApprover)
	if err := validateGovernanceTimeChain(fixture.conformance, fixture.shadow, fixture.approval, fixture.activation, fixture.receipt); err != nil {
		t.Fatalf("valid time chain was rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*ConformanceArtifact, *ShadowArtifact, *ApprovalArtifact, *ActivationArtifact, *ModelGovernanceReceipt)
	}{
		{name: "conformance zero duration", mutate: func(c *ConformanceArtifact, _ *ShadowArtifact, _ *ApprovalArtifact, _ *ActivationArtifact, _ *ModelGovernanceReceipt) {
			c.CompletedAt = c.StartedAt
		}},
		{name: "shadow zero duration", mutate: func(_ *ConformanceArtifact, s *ShadowArtifact, _ *ApprovalArtifact, _ *ActivationArtifact, _ *ModelGovernanceReceipt) {
			s.CompletedAt = s.StartedAt
		}},
		{name: "shadow before conformance completion", mutate: func(c *ConformanceArtifact, s *ShadowArtifact, _ *ApprovalArtifact, _ *ActivationArtifact, _ *ModelGovernanceReceipt) {
			completed, _ := parseGovernanceTime(c.CompletedAt, "test")
			s.StartedAt = formatGovernanceTime(completed.Add(-time.Millisecond))
		}},
		{name: "approval before conformance completion", mutate: func(c *ConformanceArtifact, _ *ShadowArtifact, a *ApprovalArtifact, _ *ActivationArtifact, _ *ModelGovernanceReceipt) {
			completed, _ := parseGovernanceTime(c.CompletedAt, "test")
			a.IssuedAt = formatGovernanceTime(completed.Add(-time.Millisecond))
		}},
		{name: "activation before shadow completion", mutate: func(_ *ConformanceArtifact, s *ShadowArtifact, _ *ApprovalArtifact, a *ActivationArtifact, _ *ModelGovernanceReceipt) {
			completed, _ := parseGovernanceTime(s.CompletedAt, "test")
			a.IssuedAt = formatGovernanceTime(completed.Add(-time.Millisecond))
		}},
		{name: "receipt equal activation", mutate: func(_ *ConformanceArtifact, _ *ShadowArtifact, _ *ApprovalArtifact, a *ActivationArtifact, r *ModelGovernanceReceipt) {
			r.IssuedAt = a.IssuedAt
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			conformance, shadow, approval, activation, receipt := fixture.conformance, fixture.shadow, fixture.approval, fixture.activation, fixture.receipt
			test.mutate(&conformance, &shadow, &approval, &activation, &receipt)
			if err := validateGovernanceTimeChain(conformance, shadow, approval, activation, receipt); err == nil {
				t.Fatal("invalid time boundary was accepted")
			}
		})
	}
	conformance := fixture.conformance
	conformance.CompletedAt = conformance.StartedAt
	if _, err := CanonicalConformanceArtifactJSON(conformance); err == nil {
		t.Fatal("parser-level conformance zero duration was accepted")
	}
	shadow := fixture.shadow
	shadow.CompletedAt = shadow.StartedAt
	if _, err := CanonicalShadowArtifactJSON(shadow); err == nil {
		t.Fatal("parser-level shadow zero duration was accepted")
	}
}

func governanceTestNow() time.Time {
	return time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
}

func newGovernanceTestKeys(t *testing.T, now time.Time, revokedRole string, revokedAt time.Time) governanceTestKeys {
	t.Helper()
	result := governanceTestKeys{private: map[string]ed25519.PrivateKey{}}
	policy := GovernanceTrustPolicy{Signers: map[string]GovernanceSignerTrust{}}
	for _, role := range governanceRoles {
		seed := sha256.Sum256([]byte("model-governance-test-key:" + role))
		private := ed25519.NewKeyFromSeed(seed[:])
		policy.Signers[role] = GovernanceSignerTrust{
			Identity: role + "-identity", Role: role, PublicKey: private.Public().(ed25519.PublicKey),
			NotBefore: now.Add(-48 * time.Hour), NotAfter: now.Add(72 * time.Hour),
		}
		result.private[role] = private
	}
	hash, err := GovernanceTrustPolicyHash(policy)
	if err != nil {
		t.Fatalf("hash governance trust policy: %v", err)
	}
	policy.PolicyHash = hash
	if err := ValidateGovernanceTrustPolicy(policy); err != nil {
		t.Fatalf("validate governance trust policy fixture: %v", err)
	}
	result.policy = policy
	signerRevocations := []GovernanceSignerRevocation{}
	if revokedRole != "" {
		signer := policy.Signers[revokedRole]
		signerRevocations = append(signerRevocations, GovernanceSignerRevocation{
			PolicyHash: policy.PolicyHash, KeyID: revokedRole, PublicKeyHash: sha256Digest(signer.PublicKey),
			ReasonHash: testDigest("signer-revocation-" + revokedRole), RevokedAt: revokedAt.UTC(),
		})
	}
	revocations := GovernanceRevocationAuthority{
		Epoch: 1, IssuedAt: now.Add(-time.Minute), ExpiresAt: now.Add(4 * time.Minute),
		DigestRevocations: []GovernanceRevocation{}, SignerRevocations: signerRevocations,
	}
	revocationHash, err := GovernanceRevocationAuthorityHash(revocations)
	if err != nil {
		t.Fatalf("hash governance revocation authority: %v", err)
	}
	revocations.AuthorityHash = revocationHash
	if err := ValidateGovernanceRevocationAuthority(revocations, now); err != nil {
		t.Fatalf("validate governance revocation authority fixture: %v", err)
	}
	result.revocations = revocations
	return result
}

func newRotatedGovernanceTestKeys(t *testing.T, now time.Time, namespace string) governanceTestKeys {
	t.Helper()
	result := newGovernanceTestKeys(t, now, "", time.Time{})
	result.private = map[string]ed25519.PrivateKey{}
	policy := GovernanceTrustPolicy{Signers: map[string]GovernanceSignerTrust{}}
	for _, role := range governanceRoles {
		seed := sha256.Sum256([]byte("model-governance-rotated-test-key:" + namespace + ":" + role))
		private := ed25519.NewKeyFromSeed(seed[:])
		policy.Signers[role] = GovernanceSignerTrust{
			Identity: namespace + "-" + role + "-identity", Role: role, PublicKey: private.Public().(ed25519.PublicKey),
			NotBefore: now.Add(-48 * time.Hour), NotAfter: now.Add(72 * time.Hour),
		}
		result.private[role] = private
	}
	policyHash, err := GovernanceTrustPolicyHash(policy)
	if err != nil {
		t.Fatalf("hash rotated governance trust policy: %v", err)
	}
	policy.PolicyHash = policyHash
	if err := ValidateGovernanceTrustPolicy(policy); err != nil {
		t.Fatalf("validate rotated governance trust policy: %v", err)
	}
	result.policy = policy
	return result
}

func buildGovernanceFixture(
	t *testing.T,
	now time.Time,
	keys governanceTestKeys,
	profile ModelProfile,
	generation uint64,
	previousFence string,
	baseline BaselineBinding,
	approvalSignerRole string,
) governanceFixture {
	t.Helper()
	route := ProviderRouteAuthority{
		EgressPolicyHash: testDigest("egress-policy"), EndpointDigest: testDigest("provider-endpoint"),
		ExpiresAt: formatGovernanceTime(now.Add(10 * time.Hour)), IssuedAt: formatGovernanceTime(now.Add(-24 * time.Hour)),
		Protocol: profile.Provider.Protocol, RouteID: profile.Provider.RouteID, SchemaVersion: ProviderRouteAuthoritySchemaV1,
		TLSIdentityHash: testDigest("provider-tls-identity"),
	}
	routeJSON, err := CanonicalProviderRouteAuthorityJSON(route)
	if err != nil {
		t.Fatalf("canonical route authority fixture: %v", err)
	}
	profile.Provider.RouteAuthorityHash = sha256Digest(routeJSON)
	profileJSON := mustCanonicalProfile(t, profile)
	profileHash := sha256Digest(profileJSON)
	corpus := validFrozenCorpus(t, profile)
	corpusJSON := mustCanonicalCorpus(t, corpus)
	subject := GovernanceSubjectBinding{
		Corpus:      GovernanceCorpusBinding{ContentHash: sha256Digest(corpusJSON), ID: corpus.ID},
		HarnessHash: corpus.HarnessHash, Profile: CorpusProfileBinding{ID: profile.ID, ContentHash: profileHash, Workload: profile.Workload},
		ProviderRoute:       GovernanceProviderRouteBinding{AuthorityHash: sha256Digest(routeJSON), RouteID: profile.Provider.RouteID},
		Runner:              profile.Runner,
		Source:              GovernanceSourceBinding{Commit: strings.Repeat("b", 40), Dirty: false, TreeDigest: testDigest("governance-source-tree"), TreeDigestSchema: SourceTreeDigestSchemaV1},
		ThresholdPolicyHash: corpus.ThresholdPolicyHash, TrustPolicyHash: keys.policy.PolicyHash, VerifierHash: corpus.VerifierHash,
	}

	conformance := ConformanceArtifact{
		ArtifactID: "60000000-0000-4000-8000-000000000001", CompletedAt: formatGovernanceTime(now.Add(-9 * time.Hour)),
		ExpiresAt: formatGovernanceTime(now.Add(20 * time.Hour)), IssuedAt: formatGovernanceTime(now.Add(-8 * time.Hour)),
		Result: ConformanceResultPassed, ResultHash: testDigest("conformance-result"), SchemaVersion: ConformanceArtifactSchemaVersion,
		StartedAt: formatGovernanceTime(now.Add(-10 * time.Hour)), Subject: subject,
	}
	conformancePayload, err := CanonicalConformanceArtifactJSON(conformance)
	if err != nil {
		t.Fatalf("canonical conformance fixture: %v", err)
	}
	conformanceEnvelope, conformanceRef := signGovernanceFixture(t, conformance.ArtifactID, GovernanceEnvelopePayloadTypeConformance, conformancePayload, RoleConformanceVerifier, keys.private[RoleConformanceVerifier])

	shadow := ShadowArtifact{
		ArtifactID: "60000000-0000-4000-8000-000000000002", Baseline: baseline, ComparisonHash: testDigest("shadow-comparison"),
		CompletedAt: formatGovernanceTime(now.Add(-7 * time.Hour)), ExpiresAt: formatGovernanceTime(now.Add(18 * time.Hour)),
		IssuedAt: formatGovernanceTime(now.Add(-6 * time.Hour)), Result: ShadowResultPassed, SchemaVersion: ShadowArtifactSchemaVersion,
		StartedAt: formatGovernanceTime(now.Add(-8 * time.Hour)), Subject: subject,
	}
	shadowPayload, err := CanonicalShadowArtifactJSON(shadow)
	if err != nil {
		t.Fatalf("canonical shadow fixture: %v", err)
	}
	shadowEnvelope, shadowRef := signGovernanceFixture(t, shadow.ArtifactID, GovernanceEnvelopePayloadTypeShadow, shadowPayload, RoleShadowVerifier, keys.private[RoleShadowVerifier])

	approval := ApprovalArtifact{
		ArtifactID: "60000000-0000-4000-8000-000000000003", Conformance: conformanceRef, Decision: ApprovalDecisionApprove,
		DecisionHash: testDigest("approval-decision"), ExpiresAt: formatGovernanceTime(now.Add(16 * time.Hour)),
		IssuedAt: formatGovernanceTime(now.Add(-5 * time.Hour)), SchemaVersion: ApprovalArtifactSchemaVersion, Subject: subject,
	}
	approvalPayload, err := CanonicalApprovalArtifactJSON(approval)
	if err != nil {
		t.Fatalf("canonical approval fixture: %v", err)
	}
	approvalEnvelope, approvalRef := signGovernanceFixture(t, approval.ArtifactID, GovernanceEnvelopePayloadTypeApproval, approvalPayload, approvalSignerRole, keys.private[approvalSignerRole])

	activation := ActivationArtifact{
		Approval: approvalRef, ArtifactID: "60000000-0000-4000-8000-000000000004", Conformance: conformanceRef,
		Decision: ActivationDecisionApply, DecisionHash: testDigest("activation-decision"), ExpiresAt: formatGovernanceTime(now.Add(14 * time.Hour)),
		Fence: testDigest("activation-fence-" + profile.ID), Generation: generation, IssuedAt: formatGovernanceTime(now.Add(-4 * time.Hour)),
		PreviousFence: previousFence, PreviousGeneration: generation - 1, SchemaVersion: ActivationArtifactSchemaVersion,
		Shadow: shadowRef, Subject: subject,
	}
	activationPayload, err := CanonicalActivationArtifactJSON(activation)
	if err != nil {
		t.Fatalf("canonical activation fixture: %v", err)
	}
	activationEnvelope, activationRef := signGovernanceFixture(t, activation.ArtifactID, GovernanceEnvelopePayloadTypeActivation, activationPayload, RoleActivationApprover, keys.private[RoleActivationApprover])

	receipt := ModelGovernanceReceipt{
		Activation: activationRef, Approval: approvalRef, ArtifactID: "60000000-0000-4000-8000-000000000005",
		Conformance: conformanceRef, ExpiresAt: formatGovernanceTime(now.Add(12 * time.Hour)), Fence: activation.Fence,
		Generation: generation, IssuedAt: formatGovernanceTime(now.Add(-3 * time.Hour)), SchemaVersion: GovernanceReceiptSchemaVersion,
		Shadow: shadowRef, Subject: subject,
	}
	receiptPayload, err := CanonicalModelGovernanceReceiptJSON(receipt)
	if err != nil {
		t.Fatalf("canonical receipt fixture: %v", err)
	}
	receiptEnvelope, _ := signGovernanceFixture(t, receipt.ArtifactID, GovernanceEnvelopePayloadTypeReceipt, receiptPayload, RoleReceiptIssuer, keys.private[RoleReceiptIssuer])

	return governanceFixture{
		now: now, receiptDigest: sha256Digest(receiptEnvelope), profile: profile, profileHash: profileHash, corpus: corpus, route: route,
		conformance: conformance, shadow: shadow, approval: approval, activation: activation, receipt: receipt,
		conformanceRef: conformanceRef, shadowRef: shadowRef, approvalRef: approvalRef, activationRef: activationRef,
		materials: GovernanceMaterials{
			ModelProfileJSON: profileJSON, FrozenCorpusJSON: corpusJSON, ProviderRouteAuthorityJSON: routeJSON,
			ConformanceEnvelope: conformanceEnvelope, ShadowEnvelope: shadowEnvelope, ApprovalEnvelope: approvalEnvelope,
			ActivationEnvelope: activationEnvelope, ReceiptEnvelope: receiptEnvelope,
		},
	}
}

func signGovernanceFixture(t *testing.T, artifactID, payloadType string, payload []byte, keyID string, private ed25519.PrivateKey) ([]byte, GovernanceArtifactRef) {
	t.Helper()
	signature := ed25519.Sign(private, templateauthority.DSSEPAE(payloadType, payload))
	envelope := GovernanceEnvelope{
		Payload: base64.StdEncoding.EncodeToString(payload), PayloadType: payloadType,
		Signatures: []GovernanceSignature{{KeyID: keyID, Sig: base64.StdEncoding.EncodeToString(signature)}},
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("marshal signed governance fixture: %v", err)
	}
	return encoded, GovernanceArtifactRef{ArtifactID: artifactID, EnvelopeDigest: sha256Digest(encoded), PayloadDigest: sha256Digest(payload)}
}
