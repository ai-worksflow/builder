package modelgovernance

import (
	"fmt"
	"time"
)

// VerifyGenesis validates the distinct, current Genesis authority chain. It
// never accepts an ordinary activation receipt and never invents a Shadow.
func (verifier *GovernanceVerifier) VerifyGenesis(
	materials GenesisGovernanceMaterials,
	expectedReceiptDigest string,
	policy GovernanceTrustPolicy,
	revocations GovernanceRevocationAuthority,
	now time.Time,
) (VerifiedGovernance, error) {
	return verifier.verifyGenesisAt(materials, expectedReceiptDigest, policy, revocations, now, now)
}

// VerifyGenesisHistorical revalidates a persisted Genesis predecessor at its
// exact commit time while applying the current cumulative revocation ledger.
func (verifier *GovernanceVerifier) VerifyGenesisHistorical(
	materials GenesisGovernanceMaterials,
	expectedReceiptDigest string,
	policy GovernanceTrustPolicy,
	revocations GovernanceRevocationAuthority,
	trustedNow time.Time,
	activatedAt time.Time,
) (VerifiedGovernance, error) {
	return verifier.verifyGenesisAt(materials, expectedReceiptDigest, policy, revocations, trustedNow, activatedAt)
}

func (verifier *GovernanceVerifier) verifyGenesisAt(
	materials GenesisGovernanceMaterials,
	expectedReceiptDigest string,
	policy GovernanceTrustPolicy,
	revocations GovernanceRevocationAuthority,
	authorityNow time.Time,
	evidenceNow time.Time,
) (VerifiedGovernance, error) {
	if verifier == nil {
		return VerifiedGovernance{}, fmt.Errorf("%w: Genesis verifier is required", ErrGovernanceInvalid)
	}
	var err error
	authorityNow, err = normalizeGovernanceTrustedTime(authorityNow)
	if err != nil {
		return VerifiedGovernance{}, fmt.Errorf("%w: Genesis trusted time is invalid", ErrGovernanceInvalid)
	}
	evidenceNow, err = normalizeGovernanceTrustedTime(evidenceNow)
	if err != nil || evidenceNow.After(authorityNow.Add(MaximumGovernanceClockSkew)) {
		return VerifiedGovernance{}, fmt.Errorf("%w: Genesis evidence time is invalid", ErrGovernanceInvalid)
	}
	if err := ValidateGovernanceTrustPolicy(policy); err != nil {
		return VerifiedGovernance{}, err
	}
	if err := ValidateGovernanceRevocationAuthority(revocations, authorityNow); err != nil {
		return VerifiedGovernance{}, err
	}

	receiptEnvelope, err := parseGovernanceEnvelope(
		materials.ReceiptEnvelope, expectedReceiptDigest, GovernanceEnvelopePayloadTypeGenesisReceipt,
	)
	if err != nil {
		return VerifiedGovernance{}, err
	}
	receipt, err := ParseModelGovernanceGenesisReceipt(receiptEnvelope.payload, receiptEnvelope.payloadDigest)
	if err != nil {
		return VerifiedGovernance{}, err
	}
	if err := verifier.verifySignedEnvelope(receiptEnvelope, receipt.IssuedAt, receipt.ExpiresAt, RoleReceiptIssuer, policy, revocations, evidenceNow); err != nil {
		return VerifiedGovernance{}, err
	}
	conformanceEnvelope, conformance, err := verifier.verifyConformance(
		materials.ConformanceEnvelope, receipt.Conformance, policy, revocations, evidenceNow,
	)
	if err != nil {
		return VerifiedGovernance{}, err
	}
	approvalEnvelope, approval, err := verifier.verifyApproval(
		materials.ApprovalEnvelope, receipt.Approval, policy, revocations, evidenceNow,
	)
	if err != nil {
		return VerifiedGovernance{}, err
	}
	genesisEnvelope, err := parseGovernanceEnvelope(
		materials.GenesisEnvelope, receipt.Genesis.EnvelopeDigest, GovernanceEnvelopePayloadTypeGenesis,
	)
	if err != nil || genesisEnvelope.payloadDigest != receipt.Genesis.PayloadDigest {
		return VerifiedGovernance{}, fmt.Errorf("%w: Genesis decision envelope reference drift", ErrGovernanceInvalid)
	}
	genesis, err := ParseGovernanceGenesisArtifact(genesisEnvelope.payload, receipt.Genesis.PayloadDigest)
	if err != nil || genesis.ArtifactID != receipt.Genesis.ArtifactID {
		return VerifiedGovernance{}, fmt.Errorf("%w: Genesis decision reference drift", ErrGovernanceInvalid)
	}
	if err := verifier.verifySignedEnvelope(genesisEnvelope, genesis.IssuedAt, genesis.ExpiresAt, RoleGenesisApprover, policy, revocations, evidenceNow); err != nil {
		return VerifiedGovernance{}, err
	}

	if conformance.Subject != receipt.Subject || approval.Subject != receipt.Subject || genesis.Subject != receipt.Subject {
		return VerifiedGovernance{}, fmt.Errorf("%w: Genesis artifacts do not share one exact subject", ErrGovernanceInvalid)
	}
	if receipt.Subject.TrustPolicyHash != policy.PolicyHash {
		return VerifiedGovernance{}, fmt.Errorf("%w: Genesis does not bind the supplied signer policy", ErrGovernanceUntrusted)
	}
	actualConformanceRef := governanceRef(conformance.ArtifactID, conformanceEnvelope)
	actualApprovalRef := governanceRef(approval.ArtifactID, approvalEnvelope)
	actualGenesisRef := governanceRef(genesis.ArtifactID, genesisEnvelope)
	if receipt.Conformance != actualConformanceRef || receipt.Approval != actualApprovalRef || receipt.Genesis != actualGenesisRef ||
		approval.Conformance != actualConformanceRef || genesis.Conformance != actualConformanceRef || genesis.Approval != actualApprovalRef {
		return VerifiedGovernance{}, fmt.Errorf("%w: Genesis artifact reference closure failed", ErrGovernanceInvalid)
	}
	if receipt.Generation != genesis.Generation || receipt.Fence != genesis.Fence ||
		receipt.RevocationAuthority != genesis.RevocationAuthority ||
		genesis.RevocationAuthority.Epoch > revocations.Epoch ||
		(genesis.RevocationAuthority.Epoch == revocations.Epoch && genesis.RevocationAuthority.AuthorityHash != revocations.AuthorityHash) {
		return VerifiedGovernance{}, fmt.Errorf("%w: Genesis generation, fence, or current revocation authority drifted", ErrGovernanceUntrusted)
	}
	if err := validateGenesisTimeChain(conformance, approval, genesis, receipt); err != nil {
		return VerifiedGovernance{}, err
	}

	profile, err := ParseModelProfile(materials.ModelProfileJSON, receipt.Subject.Profile.ContentHash)
	if err != nil {
		return VerifiedGovernance{}, fmt.Errorf("%w: exact Genesis ModelProfile: %v", ErrGovernanceInvalid, err)
	}
	corpus, err := ParseFrozenCorpusForProfile(materials.FrozenCorpusJSON, receipt.Subject.Corpus.ContentHash, profile)
	if err != nil {
		return VerifiedGovernance{}, fmt.Errorf("%w: exact Genesis FrozenCorpus: %v", ErrGovernanceInvalid, err)
	}
	if receipt.Subject.Profile.ID != profile.ID || receipt.Subject.Profile.Workload != profile.Workload ||
		receipt.Subject.Corpus.ID != corpus.ID || receipt.Subject.ThresholdPolicyHash != corpus.ThresholdPolicyHash ||
		receipt.Subject.HarnessHash != corpus.HarnessHash || receipt.Subject.VerifierHash != corpus.VerifierHash ||
		receipt.Subject.ProviderRoute.RouteID != profile.Provider.RouteID ||
		receipt.Subject.ProviderRoute.AuthorityHash != profile.Provider.RouteAuthorityHash || receipt.Subject.Runner != profile.Runner {
		return VerifiedGovernance{}, fmt.Errorf("%w: Genesis profile, corpus, evaluator, route, or runner closure failed", ErrGovernanceInvalid)
	}
	route, err := ParseProviderRouteAuthority(materials.ProviderRouteAuthorityJSON, receipt.Subject.ProviderRoute.AuthorityHash)
	if err != nil || route.RouteID != profile.Provider.RouteID || route.Protocol != profile.Provider.Protocol {
		return VerifiedGovernance{}, fmt.Errorf("%w: Genesis provider route does not match the exact ModelProfile", ErrGovernanceInvalid)
	}
	if err := requireGovernanceWindowCurrent(route.IssuedAt, route.ExpiresAt, evidenceNow, "Genesis provider route authority"); err != nil {
		return VerifiedGovernance{}, err
	}

	digests := []string{
		receiptEnvelope.envelopeDigest, receiptEnvelope.payloadDigest,
		conformanceEnvelope.envelopeDigest, conformanceEnvelope.payloadDigest, conformance.ResultHash,
		approvalEnvelope.envelopeDigest, approvalEnvelope.payloadDigest, approval.DecisionHash,
		genesisEnvelope.envelopeDigest, genesisEnvelope.payloadDigest, genesis.DecisionHash,
		receipt.Subject.Profile.ContentHash, receipt.Subject.Corpus.ContentHash,
		receipt.Subject.ProviderRoute.AuthorityHash, receipt.Subject.ThresholdPolicyHash,
		receipt.Subject.HarnessHash, receipt.Subject.VerifierHash, receipt.Subject.Runner.ImmutableDigest,
		receipt.Subject.Source.TreeDigest, receipt.Subject.TrustPolicyHash, genesis.RevocationAuthority.AuthorityHash,
		profile.Execution.PolicyHash, profile.Execution.ParametersHash, profile.Execution.PromptHash,
		profile.Execution.SchemaHash, profile.Execution.ToolchainHash,
		route.EndpointDigest, route.TLSIdentityHash, route.EgressPolicyHash,
	}
	for _, fallback := range profile.Fallback.Profiles {
		digests = append(digests, fallback.ContentHash)
	}
	for _, corpusCase := range corpus.Cases {
		digests = append(digests,
			corpusCase.Input.ContentHash, corpusCase.HiddenOracle.CiphertextHash,
			corpusCase.HiddenOracle.PlaintextCommitmentHash, corpusCase.HiddenOracle.KeyPolicyHash,
			corpusCase.BuildContract.ContentHash, corpusCase.BuildContract.ContractHash,
			corpusCase.TemplateRelease.ContentHash, corpusCase.TemplateRelease.ApprovalReceiptDigest,
			corpusCase.BaseTree.TreeDigest,
		)
	}
	if err := requireNotRevoked(revocations, evidenceNow, digests...); err != nil {
		return VerifiedGovernance{}, err
	}

	return VerifiedGovernance{
		AuthorityKind: GenesisAuthorityKind,
		Profile:       profile, Corpus: corpus, ProviderRoute: route, Subject: receipt.Subject,
		Conformance: conformance, Approval: approval, Genesis: genesis, GenesisReceipt: receipt,
		ConformanceRef: actualConformanceRef, ApprovalRef: actualApprovalRef, GenesisRef: actualGenesisRef,
		ReceiptEnvelopeDigest: receiptEnvelope.envelopeDigest, ReceiptPayloadDigest: receiptEnvelope.payloadDigest,
		SignerIdentities: map[string]string{
			RoleConformanceVerifier: policy.Signers[conformanceEnvelope.envelope.Signatures[0].KeyID].Identity,
			RoleProfileApprover:     policy.Signers[approvalEnvelope.envelope.Signatures[0].KeyID].Identity,
			RoleGenesisApprover:     policy.Signers[genesisEnvelope.envelope.Signatures[0].KeyID].Identity,
			RoleReceiptIssuer:       policy.Signers[receiptEnvelope.envelope.Signatures[0].KeyID].Identity,
		},
	}, nil
}

func validateGenesisTimeChain(
	conformance ConformanceArtifact,
	approval ApprovalArtifact,
	genesis GovernanceGenesisArtifact,
	receipt ModelGovernanceGenesisReceipt,
) error {
	conformanceCompleted, _ := parseGovernanceTime(conformance.CompletedAt, "conformance.completedAt")
	conformanceIssued, _ := parseGovernanceTime(conformance.IssuedAt, "conformance.issuedAt")
	conformanceExpires, _ := parseGovernanceTime(conformance.ExpiresAt, "conformance.expiresAt")
	approvalIssued, _ := parseGovernanceTime(approval.IssuedAt, "approval.issuedAt")
	approvalExpires, _ := parseGovernanceTime(approval.ExpiresAt, "approval.expiresAt")
	genesisIssued, _ := parseGovernanceTime(genesis.IssuedAt, "genesis.issuedAt")
	genesisExpires, _ := parseGovernanceTime(genesis.ExpiresAt, "genesis.expiresAt")
	receiptIssued, _ := parseGovernanceTime(receipt.IssuedAt, "genesisReceipt.issuedAt")
	receiptExpires, _ := parseGovernanceTime(receipt.ExpiresAt, "genesisReceipt.expiresAt")
	if approvalIssued.Before(conformanceCompleted) || approvalIssued.Before(conformanceIssued) ||
		genesisIssued.Before(approvalIssued) || genesisIssued.Before(conformanceIssued) ||
		!receiptIssued.After(genesisIssued) || approvalExpires.After(conformanceExpires) ||
		genesisExpires.After(conformanceExpires) || genesisExpires.After(approvalExpires) || receiptExpires.After(genesisExpires) {
		return fmt.Errorf("%w: Genesis issuance or expiry chain is not monotonic", ErrGovernanceInvalid)
	}
	return nil
}
