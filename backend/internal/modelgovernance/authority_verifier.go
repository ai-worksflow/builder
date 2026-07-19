package modelgovernance

import (
	"bytes"
	"crypto/ed25519"
	"fmt"
	"regexp"
	"sort"
	"time"

	"github.com/worksflow/builder/backend/internal/templateauthority"
)

var governanceIdentityPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:@/+~-]{0,511}$`)

var governanceRoles = []string{
	RoleActivationApprover,
	RoleConformanceVerifier,
	RoleGenesisApprover,
	RoleProfileApprover,
	RoleReceiptIssuer,
	RoleShadowVerifier,
}

type GovernanceVerifier struct{}

func NewGovernanceVerifier() *GovernanceVerifier { return &GovernanceVerifier{} }

func ValidateGovernanceTrustPolicy(policy GovernanceTrustPolicy) error {
	if !validDigest(policy.PolicyHash) || len(policy.Signers) < len(governanceRoles) || len(policy.Signers) > 32 {
		return fmt.Errorf("%w: trust policy digest or signer set is invalid", ErrGovernanceUntrusted)
	}
	if err := validateGovernanceTrustPolicyContents(policy); err != nil {
		return err
	}
	actualHash, err := GovernanceTrustPolicyHash(policy)
	if err != nil || actualHash != policy.PolicyHash {
		return fmt.Errorf("%w: trust policy hash does not commit its canonical immutable signer bytes", ErrGovernanceUntrusted)
	}
	return nil
}

func validateGovernanceTrustPolicyContents(policy GovernanceTrustPolicy) error {
	if len(policy.Signers) < len(governanceRoles) || len(policy.Signers) > 32 {
		return fmt.Errorf("%w: signer set is invalid", ErrGovernanceUntrusted)
	}
	roleSet := make(map[string]struct{}, len(governanceRoles))
	for _, role := range governanceRoles {
		roleSet[role] = struct{}{}
	}
	roleCounts := make(map[string]int, len(roleSet))
	identities := map[string]string{}
	keyFingerprints := map[string]string{}
	keyIDs := make([]string, 0, len(policy.Signers))
	for keyID := range policy.Signers {
		keyIDs = append(keyIDs, keyID)
	}
	sort.Strings(keyIDs)
	for _, keyID := range keyIDs {
		signer := policy.Signers[keyID]
		if !validStableID(keyID) || !governanceIdentityPattern.MatchString(signer.Identity) {
			return fmt.Errorf("%w: signer %q identity is invalid", ErrGovernanceUntrusted, keyID)
		}
		if _, exists := roleSet[signer.Role]; !exists {
			return fmt.Errorf("%w: signer %q role is not in the closed set", ErrGovernanceUntrusted, keyID)
		}
		if len(signer.PublicKey) != ed25519.PublicKeySize || !canonicalGovernanceTime(signer.NotBefore) || !canonicalGovernanceTime(signer.NotAfter) || !signer.NotAfter.After(signer.NotBefore) {
			return fmt.Errorf("%w: signer %q key or validity interval is invalid", ErrGovernanceUntrusted, keyID)
		}
		if previous, duplicate := identities[signer.Identity]; duplicate {
			return fmt.Errorf("%w: signer identity is shared by %q and %q", ErrGovernanceUntrusted, previous, keyID)
		}
		identities[signer.Identity] = keyID
		fingerprint := sha256Digest(signer.PublicKey)
		if previous, duplicate := keyFingerprints[fingerprint]; duplicate {
			return fmt.Errorf("%w: public key is shared by %q and %q", ErrGovernanceUntrusted, previous, keyID)
		}
		keyFingerprints[fingerprint] = keyID
		roleCounts[signer.Role]++
	}
	for _, role := range governanceRoles {
		if roleCounts[role] == 0 {
			return fmt.Errorf("%w: role %q has no independent signer", ErrGovernanceUntrusted, role)
		}
	}
	return nil
}

func ValidateGovernanceRevocationAuthority(authority GovernanceRevocationAuthority, now time.Time) error {
	normalized, err := normalizeGovernanceTrustedTime(now)
	if err != nil {
		return err
	}
	if !validDigest(authority.AuthorityHash) {
		return fmt.Errorf("%w: revocation authority hash is invalid", ErrGovernanceUntrusted)
	}
	if err := validateGovernanceRevocationAuthorityContents(authority); err != nil {
		return err
	}
	actualHash, err := GovernanceRevocationAuthorityHash(authority)
	if err != nil || actualHash != authority.AuthorityHash {
		return fmt.Errorf("%w: revocation authority hash does not commit its canonical epoch and entries", ErrGovernanceUntrusted)
	}
	if normalized.Before(authority.IssuedAt.Add(-MaximumGovernanceClockSkew)) || !normalized.Before(authority.ExpiresAt) {
		return fmt.Errorf("%w: revocation authority is not current", ErrGovernanceUntrusted)
	}
	return nil
}

func validateGovernanceRevocationAuthorityContents(authority GovernanceRevocationAuthority) error {
	if authority.Epoch == 0 || authority.DigestRevocations == nil || authority.SignerRevocations == nil ||
		!canonicalGovernanceTime(authority.IssuedAt) || !canonicalGovernanceTime(authority.ExpiresAt) ||
		!authority.ExpiresAt.After(authority.IssuedAt) || authority.ExpiresAt.Sub(authority.IssuedAt) > MaximumRevocationAuthorityLifetime ||
		len(authority.DigestRevocations) > 4096 || len(authority.SignerRevocations) > 1024 {
		return fmt.Errorf("%w: revocation authority epoch, window, or entry count is invalid", ErrGovernanceUntrusted)
	}
	priorDigest := ""
	for index, revocation := range authority.DigestRevocations {
		if !validDigest(revocation.Digest) || !validDigest(revocation.ReasonHash) || !canonicalGovernanceTime(revocation.RevokedAt) ||
			(index > 0 && priorDigest >= revocation.Digest) {
			return fmt.Errorf("%w: digest revocations must be canonical, unique, and sorted", ErrGovernanceUntrusted)
		}
		priorDigest = revocation.Digest
	}
	priorSigner := ""
	for index, revocation := range authority.SignerRevocations {
		selector := revocation.PolicyHash + "\x00" + revocation.KeyID
		if !validDigest(revocation.PolicyHash) || !validStableID(revocation.KeyID) || !validDigest(revocation.PublicKeyHash) ||
			!validDigest(revocation.ReasonHash) || !canonicalGovernanceTime(revocation.RevokedAt) ||
			(index > 0 && priorSigner >= selector) {
			return fmt.Errorf("%w: signer revocations must be canonical, unique, and sorted", ErrGovernanceUntrusted)
		}
		priorSigner = selector
	}
	return nil
}

func (verifier *GovernanceVerifier) Verify(
	materials GovernanceMaterials,
	expectedReceiptDigest string,
	policy GovernanceTrustPolicy,
	revocations GovernanceRevocationAuthority,
	now time.Time,
) (VerifiedGovernance, error) {
	return verifier.verifyAt(materials, expectedReceiptDigest, policy, revocations, now, now)
}

// VerifyHistorical revalidates an already persisted predecessor at its exact
// activation time while applying the current cumulative revocation ledger at
// that historical instant. It is for replacement only and must never authorize
// a provider execution.
func (verifier *GovernanceVerifier) VerifyHistorical(
	materials GovernanceMaterials,
	expectedReceiptDigest string,
	policy GovernanceTrustPolicy,
	revocations GovernanceRevocationAuthority,
	trustedNow time.Time,
	activatedAt time.Time,
) (VerifiedGovernance, error) {
	return verifier.verifyAt(materials, expectedReceiptDigest, policy, revocations, trustedNow, activatedAt)
}

func (verifier *GovernanceVerifier) verifyAt(
	materials GovernanceMaterials,
	expectedReceiptDigest string,
	policy GovernanceTrustPolicy,
	revocations GovernanceRevocationAuthority,
	authorityNow time.Time,
	evidenceNow time.Time,
) (VerifiedGovernance, error) {
	if verifier == nil {
		return VerifiedGovernance{}, fmt.Errorf("%w: verifier and trusted time are required", ErrGovernanceInvalid)
	}
	var err error
	authorityNow, err = normalizeGovernanceTrustedTime(authorityNow)
	if err != nil {
		return VerifiedGovernance{}, fmt.Errorf("%w: verifier and trusted time are required", ErrGovernanceInvalid)
	}
	evidenceNow, err = normalizeGovernanceTrustedTime(evidenceNow)
	if err != nil || evidenceNow.After(authorityNow.Add(MaximumGovernanceClockSkew)) {
		return VerifiedGovernance{}, fmt.Errorf("%w: evidence evaluation time is invalid", ErrGovernanceInvalid)
	}
	if err := ValidateGovernanceTrustPolicy(policy); err != nil {
		return VerifiedGovernance{}, err
	}
	if err := ValidateGovernanceRevocationAuthority(revocations, authorityNow); err != nil {
		return VerifiedGovernance{}, err
	}
	now := evidenceNow

	receiptEnvelope, err := parseGovernanceEnvelope(materials.ReceiptEnvelope, expectedReceiptDigest, GovernanceEnvelopePayloadTypeReceipt)
	if err != nil {
		return VerifiedGovernance{}, err
	}
	receipt, err := ParseModelGovernanceReceipt(receiptEnvelope.payload, receiptEnvelope.payloadDigest)
	if err != nil {
		return VerifiedGovernance{}, err
	}
	if err := verifier.verifySignedEnvelope(receiptEnvelope, receipt.IssuedAt, receipt.ExpiresAt, RoleReceiptIssuer, policy, revocations, now); err != nil {
		return VerifiedGovernance{}, err
	}

	conformanceEnvelope, conformance, err := verifier.verifyConformance(materials.ConformanceEnvelope, receipt.Conformance, policy, revocations, now)
	if err != nil {
		return VerifiedGovernance{}, err
	}
	shadowEnvelope, shadow, err := verifier.verifyShadow(materials.ShadowEnvelope, receipt.Shadow, policy, revocations, now)
	if err != nil {
		return VerifiedGovernance{}, err
	}
	approvalEnvelope, approval, err := verifier.verifyApproval(materials.ApprovalEnvelope, receipt.Approval, policy, revocations, now)
	if err != nil {
		return VerifiedGovernance{}, err
	}
	activationEnvelope, activation, err := verifier.verifyActivation(materials.ActivationEnvelope, receipt.Activation, policy, revocations, now)
	if err != nil {
		return VerifiedGovernance{}, err
	}

	if conformance.Subject != receipt.Subject || shadow.Subject != receipt.Subject || approval.Subject != receipt.Subject || activation.Subject != receipt.Subject {
		return VerifiedGovernance{}, fmt.Errorf("%w: governance artifacts do not share one exact subject", ErrGovernanceInvalid)
	}
	if receipt.Subject.TrustPolicyHash != policy.PolicyHash {
		return VerifiedGovernance{}, fmt.Errorf("%w: governance subject does not bind the exact supplied signer policy", ErrGovernanceUntrusted)
	}
	actualConformanceRef := governanceRef(conformance.ArtifactID, conformanceEnvelope)
	actualShadowRef := governanceRef(shadow.ArtifactID, shadowEnvelope)
	actualApprovalRef := governanceRef(approval.ArtifactID, approvalEnvelope)
	actualActivationRef := governanceRef(activation.ArtifactID, activationEnvelope)
	if receipt.Conformance != actualConformanceRef || receipt.Shadow != actualShadowRef || receipt.Approval != actualApprovalRef || receipt.Activation != actualActivationRef ||
		approval.Conformance != actualConformanceRef || activation.Conformance != actualConformanceRef || activation.Shadow != actualShadowRef || activation.Approval != actualApprovalRef {
		return VerifiedGovernance{}, fmt.Errorf("%w: governance artifact reference closure failed", ErrGovernanceInvalid)
	}
	if receipt.Generation != activation.Generation || receipt.Fence != activation.Fence {
		return VerifiedGovernance{}, fmt.Errorf("%w: receipt generation or fence differs from activation decision", ErrGovernanceInvalid)
	}
	if err := validateGovernanceTimeChain(conformance, shadow, approval, activation, receipt); err != nil {
		return VerifiedGovernance{}, err
	}

	profile, err := ParseModelProfile(materials.ModelProfileJSON, receipt.Subject.Profile.ContentHash)
	if err != nil {
		return VerifiedGovernance{}, fmt.Errorf("%w: exact ModelProfile: %v", ErrGovernanceInvalid, err)
	}
	corpus, err := ParseFrozenCorpusForProfile(materials.FrozenCorpusJSON, receipt.Subject.Corpus.ContentHash, profile)
	if err != nil {
		return VerifiedGovernance{}, fmt.Errorf("%w: exact FrozenCorpus: %v", ErrGovernanceInvalid, err)
	}
	if receipt.Subject.Profile.ID != profile.ID || receipt.Subject.Profile.Workload != profile.Workload || receipt.Subject.Corpus.ID != corpus.ID ||
		receipt.Subject.ThresholdPolicyHash != corpus.ThresholdPolicyHash || receipt.Subject.HarnessHash != corpus.HarnessHash || receipt.Subject.VerifierHash != corpus.VerifierHash ||
		receipt.Subject.ProviderRoute.RouteID != profile.Provider.RouteID || receipt.Subject.ProviderRoute.AuthorityHash != profile.Provider.RouteAuthorityHash ||
		receipt.Subject.Runner != profile.Runner {
		return VerifiedGovernance{}, fmt.Errorf("%w: profile, corpus, evaluator, route, or runner binding closure failed", ErrGovernanceInvalid)
	}
	route, err := ParseProviderRouteAuthority(materials.ProviderRouteAuthorityJSON, receipt.Subject.ProviderRoute.AuthorityHash)
	if err != nil {
		return VerifiedGovernance{}, err
	}
	if route.RouteID != profile.Provider.RouteID || route.Protocol != profile.Provider.Protocol {
		return VerifiedGovernance{}, fmt.Errorf("%w: ProviderRouteAuthority does not match the exact ModelProfile", ErrGovernanceInvalid)
	}
	if err := requireGovernanceWindowCurrent(route.IssuedAt, route.ExpiresAt, now, "provider route authority"); err != nil {
		return VerifiedGovernance{}, err
	}
	if shadow.Baseline.Profile.Workload != profile.Workload || shadow.Baseline.Profile.ContentHash == receipt.Subject.Profile.ContentHash ||
		shadow.Baseline.ReceiptDigest == expectedReceiptDigest {
		return VerifiedGovernance{}, fmt.Errorf("%w: shadow baseline is not an independent exact baseline", ErrGovernanceInvalid)
	}

	digests := []string{
		receiptEnvelope.envelopeDigest, receiptEnvelope.payloadDigest,
		conformanceEnvelope.envelopeDigest, conformanceEnvelope.payloadDigest, conformance.ResultHash,
		shadowEnvelope.envelopeDigest, shadowEnvelope.payloadDigest, shadow.ComparisonHash, shadow.Baseline.Profile.ContentHash,
		shadow.Baseline.ReceiptDigest, shadow.Baseline.MetricsHash,
		approvalEnvelope.envelopeDigest, approvalEnvelope.payloadDigest, approval.DecisionHash,
		activationEnvelope.envelopeDigest, activationEnvelope.payloadDigest, activation.DecisionHash,
		receipt.Subject.Profile.ContentHash, receipt.Subject.Corpus.ContentHash, receipt.Subject.ProviderRoute.AuthorityHash,
		receipt.Subject.ThresholdPolicyHash, receipt.Subject.HarnessHash, receipt.Subject.VerifierHash,
		receipt.Subject.Runner.ImmutableDigest, receipt.Subject.Source.TreeDigest, receipt.Subject.TrustPolicyHash,
		profile.Execution.PolicyHash, profile.Execution.ParametersHash, profile.Execution.PromptHash,
		profile.Execution.SchemaHash, profile.Execution.ToolchainHash,
		route.EndpointDigest, route.TLSIdentityHash, route.EgressPolicyHash,
	}
	for _, fallback := range profile.Fallback.Profiles {
		digests = append(digests, fallback.ContentHash)
	}
	for _, corpusCase := range corpus.Cases {
		digests = append(digests,
			corpusCase.Input.ContentHash,
			corpusCase.HiddenOracle.CiphertextHash,
			corpusCase.HiddenOracle.PlaintextCommitmentHash,
			corpusCase.HiddenOracle.KeyPolicyHash,
			corpusCase.BuildContract.ContentHash,
			corpusCase.BuildContract.ContractHash,
			corpusCase.TemplateRelease.ContentHash,
			corpusCase.TemplateRelease.ApprovalReceiptDigest,
			corpusCase.BaseTree.TreeDigest,
		)
	}
	if err := requireNotRevoked(revocations, now, digests...); err != nil {
		return VerifiedGovernance{}, err
	}

	return VerifiedGovernance{
		AuthorityKind: ActivationAuthorityKind,
		Profile:       profile, Corpus: corpus, ProviderRoute: route, Subject: receipt.Subject,
		Conformance: conformance, Shadow: shadow, Approval: approval, Activation: activation, Receipt: receipt,
		ConformanceRef: actualConformanceRef, ShadowRef: actualShadowRef, ApprovalRef: actualApprovalRef, ActivationRef: actualActivationRef,
		ReceiptEnvelopeDigest: receiptEnvelope.envelopeDigest, ReceiptPayloadDigest: receiptEnvelope.payloadDigest,
		SignerIdentities: map[string]string{
			RoleConformanceVerifier: policy.Signers[conformanceEnvelope.envelope.Signatures[0].KeyID].Identity,
			RoleShadowVerifier:      policy.Signers[shadowEnvelope.envelope.Signatures[0].KeyID].Identity,
			RoleProfileApprover:     policy.Signers[approvalEnvelope.envelope.Signatures[0].KeyID].Identity,
			RoleActivationApprover:  policy.Signers[activationEnvelope.envelope.Signatures[0].KeyID].Identity,
			RoleReceiptIssuer:       policy.Signers[receiptEnvelope.envelope.Signatures[0].KeyID].Identity,
		},
	}, nil
}

func (verifier *GovernanceVerifier) verifyConformance(encoded []byte, reference GovernanceArtifactRef, policy GovernanceTrustPolicy, revocations GovernanceRevocationAuthority, now time.Time) (parsedGovernanceEnvelope, ConformanceArtifact, error) {
	envelope, err := parseGovernanceEnvelope(encoded, reference.EnvelopeDigest, GovernanceEnvelopePayloadTypeConformance)
	if err != nil || envelope.payloadDigest != reference.PayloadDigest {
		return parsedGovernanceEnvelope{}, ConformanceArtifact{}, fmt.Errorf("%w: conformance envelope reference drift", ErrGovernanceInvalid)
	}
	value, err := ParseConformanceArtifact(envelope.payload, reference.PayloadDigest)
	if err != nil || value.ArtifactID != reference.ArtifactID {
		return parsedGovernanceEnvelope{}, ConformanceArtifact{}, fmt.Errorf("%w: conformance artifact reference drift", ErrGovernanceInvalid)
	}
	if err := verifier.verifySignedEnvelope(envelope, value.IssuedAt, value.ExpiresAt, RoleConformanceVerifier, policy, revocations, now); err != nil {
		return parsedGovernanceEnvelope{}, ConformanceArtifact{}, err
	}
	return envelope, value, nil
}

func (verifier *GovernanceVerifier) verifyShadow(encoded []byte, reference GovernanceArtifactRef, policy GovernanceTrustPolicy, revocations GovernanceRevocationAuthority, now time.Time) (parsedGovernanceEnvelope, ShadowArtifact, error) {
	envelope, err := parseGovernanceEnvelope(encoded, reference.EnvelopeDigest, GovernanceEnvelopePayloadTypeShadow)
	if err != nil || envelope.payloadDigest != reference.PayloadDigest {
		return parsedGovernanceEnvelope{}, ShadowArtifact{}, fmt.Errorf("%w: shadow envelope reference drift", ErrGovernanceInvalid)
	}
	value, err := ParseShadowArtifact(envelope.payload, reference.PayloadDigest)
	if err != nil || value.ArtifactID != reference.ArtifactID {
		return parsedGovernanceEnvelope{}, ShadowArtifact{}, fmt.Errorf("%w: shadow artifact reference drift", ErrGovernanceInvalid)
	}
	if err := verifier.verifySignedEnvelope(envelope, value.IssuedAt, value.ExpiresAt, RoleShadowVerifier, policy, revocations, now); err != nil {
		return parsedGovernanceEnvelope{}, ShadowArtifact{}, err
	}
	return envelope, value, nil
}

func (verifier *GovernanceVerifier) verifyApproval(encoded []byte, reference GovernanceArtifactRef, policy GovernanceTrustPolicy, revocations GovernanceRevocationAuthority, now time.Time) (parsedGovernanceEnvelope, ApprovalArtifact, error) {
	envelope, err := parseGovernanceEnvelope(encoded, reference.EnvelopeDigest, GovernanceEnvelopePayloadTypeApproval)
	if err != nil || envelope.payloadDigest != reference.PayloadDigest {
		return parsedGovernanceEnvelope{}, ApprovalArtifact{}, fmt.Errorf("%w: approval envelope reference drift", ErrGovernanceInvalid)
	}
	value, err := ParseApprovalArtifact(envelope.payload, reference.PayloadDigest)
	if err != nil || value.ArtifactID != reference.ArtifactID {
		return parsedGovernanceEnvelope{}, ApprovalArtifact{}, fmt.Errorf("%w: approval artifact reference drift", ErrGovernanceInvalid)
	}
	if err := verifier.verifySignedEnvelope(envelope, value.IssuedAt, value.ExpiresAt, RoleProfileApprover, policy, revocations, now); err != nil {
		return parsedGovernanceEnvelope{}, ApprovalArtifact{}, err
	}
	return envelope, value, nil
}

func (verifier *GovernanceVerifier) verifyActivation(encoded []byte, reference GovernanceArtifactRef, policy GovernanceTrustPolicy, revocations GovernanceRevocationAuthority, now time.Time) (parsedGovernanceEnvelope, ActivationArtifact, error) {
	envelope, err := parseGovernanceEnvelope(encoded, reference.EnvelopeDigest, GovernanceEnvelopePayloadTypeActivation)
	if err != nil || envelope.payloadDigest != reference.PayloadDigest {
		return parsedGovernanceEnvelope{}, ActivationArtifact{}, fmt.Errorf("%w: activation envelope reference drift", ErrGovernanceInvalid)
	}
	value, err := ParseActivationArtifact(envelope.payload, reference.PayloadDigest)
	if err != nil || value.ArtifactID != reference.ArtifactID {
		return parsedGovernanceEnvelope{}, ActivationArtifact{}, fmt.Errorf("%w: activation artifact reference drift", ErrGovernanceInvalid)
	}
	if err := verifier.verifySignedEnvelope(envelope, value.IssuedAt, value.ExpiresAt, RoleActivationApprover, policy, revocations, now); err != nil {
		return parsedGovernanceEnvelope{}, ActivationArtifact{}, err
	}
	return envelope, value, nil
}

func (verifier *GovernanceVerifier) verifySignedEnvelope(
	envelope parsedGovernanceEnvelope,
	issuedValue, expiresValue, requiredRole string,
	policy GovernanceTrustPolicy,
	revocations GovernanceRevocationAuthority,
	now time.Time,
) error {
	if err := requireGovernanceWindowCurrent(issuedValue, expiresValue, now, requiredRole); err != nil {
		return err
	}
	issuedAt, _ := parseGovernanceTime(issuedValue, requiredRole+".issuedAt")
	signature := envelope.envelope.Signatures[0]
	trusted, exists := policy.Signers[signature.KeyID]
	if !exists || trusted.Role != requiredRole {
		return fmt.Errorf("%w: key %q cannot sign role %q", ErrGovernanceUntrusted, signature.KeyID, requiredRole)
	}
	if issuedAt.Before(trusted.NotBefore.UTC()) || now.Before(trusted.NotBefore.UTC()) || !issuedAt.Before(trusted.NotAfter.UTC()) || !now.Before(trusted.NotAfter.UTC()) ||
		signerRevokedAt(revocations, policy.PolicyHash, signature.KeyID, trusted.PublicKey, now) {
		return fmt.Errorf("%w: signer %q is expired, revoked, or invalid at issuance", ErrGovernanceUntrusted, signature.KeyID)
	}
	rawSignature, err := decodeCanonicalGovernanceBase64(signature.Sig, ed25519.SignatureSize, ed25519.SignatureSize)
	if err != nil || !ed25519.Verify(trusted.PublicKey, templateauthority.DSSEPAE(envelope.envelope.PayloadType, envelope.payload), rawSignature) {
		return fmt.Errorf("%w: signature for key %q is invalid", ErrGovernanceUntrusted, signature.KeyID)
	}
	return requireNotRevoked(revocations, now, envelope.envelopeDigest, envelope.payloadDigest)
}

func validateGovernanceTimeChain(
	conformance ConformanceArtifact,
	shadow ShadowArtifact,
	approval ApprovalArtifact,
	activation ActivationArtifact,
	receipt ModelGovernanceReceipt,
) error {
	conformanceStarted, _ := parseGovernanceTime(conformance.StartedAt, "conformance.startedAt")
	conformanceCompleted, _ := parseGovernanceTime(conformance.CompletedAt, "conformance.completedAt")
	conformanceIssued, _ := parseGovernanceTime(conformance.IssuedAt, "conformance.issuedAt")
	conformanceExpires, _ := parseGovernanceTime(conformance.ExpiresAt, "conformance.expiresAt")
	shadowStarted, _ := parseGovernanceTime(shadow.StartedAt, "shadow.startedAt")
	shadowCompleted, _ := parseGovernanceTime(shadow.CompletedAt, "shadow.completedAt")
	shadowIssued, _ := parseGovernanceTime(shadow.IssuedAt, "shadow.issuedAt")
	shadowExpires, _ := parseGovernanceTime(shadow.ExpiresAt, "shadow.expiresAt")
	approvalIssued, _ := parseGovernanceTime(approval.IssuedAt, "approval.issuedAt")
	approvalExpires, _ := parseGovernanceTime(approval.ExpiresAt, "approval.expiresAt")
	activationIssued, _ := parseGovernanceTime(activation.IssuedAt, "activation.issuedAt")
	activationExpires, _ := parseGovernanceTime(activation.ExpiresAt, "activation.expiresAt")
	receiptIssued, _ := parseGovernanceTime(receipt.IssuedAt, "receipt.issuedAt")
	receiptExpires, _ := parseGovernanceTime(receipt.ExpiresAt, "receipt.expiresAt")
	if !conformanceCompleted.After(conformanceStarted) || conformanceIssued.Before(conformanceCompleted) ||
		!shadowCompleted.After(shadowStarted) || shadowStarted.Before(conformanceCompleted) || shadowIssued.Before(shadowCompleted) ||
		approvalIssued.Before(conformanceCompleted) || approvalIssued.Before(conformanceIssued) ||
		activationIssued.Before(conformanceCompleted) || activationIssued.Before(conformanceIssued) || activationIssued.Before(shadowCompleted) ||
		activationIssued.Before(shadowIssued) || activationIssued.Before(approvalIssued) || !receiptIssued.After(activationIssued) || approvalExpires.After(conformanceExpires) ||
		activationExpires.After(conformanceExpires) || activationExpires.After(shadowExpires) || activationExpires.After(approvalExpires) ||
		receiptExpires.After(activationExpires) {
		return fmt.Errorf("%w: governance issuance or expiry chain is not monotonic", ErrGovernanceInvalid)
	}
	return nil
}

func requireGovernanceWindowCurrent(issuedValue, expiresValue string, now time.Time, label string) error {
	issuedAt, err := parseGovernanceTime(issuedValue, label+".issuedAt")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrGovernanceInvalid, err)
	}
	expiresAt, err := parseGovernanceTime(expiresValue, label+".expiresAt")
	if err != nil || now.Before(issuedAt.Add(-MaximumGovernanceClockSkew)) || !now.Before(expiresAt) {
		return fmt.Errorf("%w: %s is not current at trusted time", ErrGovernanceInvalid, label)
	}
	return nil
}

func requireNotRevoked(authority GovernanceRevocationAuthority, now time.Time, digests ...string) error {
	requested := make(map[string]struct{}, len(digests))
	for _, digest := range digests {
		requested[digest] = struct{}{}
	}
	for _, revocation := range authority.DigestRevocations {
		if _, exists := requested[revocation.Digest]; exists && !now.Before(revocation.RevokedAt.UTC()) {
			return fmt.Errorf("%w: digest %s was revoked", ErrGovernanceUntrusted, revocation.Digest)
		}
	}
	return nil
}

func signerRevokedAt(authority GovernanceRevocationAuthority, policyHash, keyID string, publicKey ed25519.PublicKey, now time.Time) bool {
	selector := policyHash + "\x00" + keyID
	index := sort.Search(len(authority.SignerRevocations), func(index int) bool {
		revocation := authority.SignerRevocations[index]
		return revocation.PolicyHash+"\x00"+revocation.KeyID >= selector
	})
	return index < len(authority.SignerRevocations) &&
		authority.SignerRevocations[index].PolicyHash == policyHash && authority.SignerRevocations[index].KeyID == keyID &&
		authority.SignerRevocations[index].PublicKeyHash == sha256Digest(publicKey) &&
		!now.Before(authority.SignerRevocations[index].RevokedAt.UTC())
}

func governanceRef(artifactID string, envelope parsedGovernanceEnvelope) GovernanceArtifactRef {
	return GovernanceArtifactRef{ArtifactID: artifactID, EnvelopeDigest: envelope.envelopeDigest, PayloadDigest: envelope.payloadDigest}
}

func cloneGovernanceTrustPolicy(policy GovernanceTrustPolicy) GovernanceTrustPolicy {
	result := GovernanceTrustPolicy{
		PolicyHash: policy.PolicyHash, Signers: make(map[string]GovernanceSignerTrust, len(policy.Signers)),
	}
	for keyID, signer := range policy.Signers {
		cloned := signer
		cloned.PublicKey = bytes.Clone(signer.PublicKey)
		result.Signers[keyID] = cloned
	}
	return result
}

func cloneGovernanceRevocationAuthority(authority GovernanceRevocationAuthority) GovernanceRevocationAuthority {
	digests := make([]GovernanceRevocation, len(authority.DigestRevocations))
	copy(digests, authority.DigestRevocations)
	signers := make([]GovernanceSignerRevocation, len(authority.SignerRevocations))
	copy(signers, authority.SignerRevocations)
	authority.DigestRevocations = digests
	authority.SignerRevocations = signers
	return authority
}
