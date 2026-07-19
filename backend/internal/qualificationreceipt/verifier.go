package qualificationreceipt

import (
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/worksflow/builder/backend/internal/goldenfault"
	"github.com/worksflow/builder/backend/internal/templateauthority"
	"golang.org/x/sys/unix"
)

const (
	maxEvidenceSnapshotDirectories = 512
	maxEvidenceSnapshotEntries     = maxArtifactCount + maxEvidenceSnapshotDirectories + 3
	maxEvidenceSnapshotPathBytes   = 1024
)

type credentialAuthority struct {
	verifier          *templateauthority.DSSEVerifier
	allowedIdentities map[string]struct{}
	keyValidity       map[string]AuthorityKeyValidity
}

type Verifier struct {
	policy                TrustPolicy
	receiptVerifier       *templateauthority.DSSEVerifier
	credentialAuthorities map[string]credentialAuthority
	encryptionAuthority   credentialAuthority
	faultAuthority        *goldenfault.Verifier
	faultAuthorityAllowed map[string]struct{}
	faultLedgerAttestor   credentialAuthority
	snapshotInspector     func(string) error
}

type signedStatement struct {
	Type          string             `json:"_type"`
	Subject       []statementSubject `json:"subject"`
	PredicateType string             `json:"predicateType"`
	Predicate     []byte             `json:"-"`
}

type rawSignedStatement struct {
	Type          string             `json:"_type"`
	Subject       []statementSubject `json:"subject"`
	PredicateType string             `json:"predicateType"`
	Predicate     jsonRaw            `json:"predicate"`
}

type jsonRaw []byte

func (raw *jsonRaw) UnmarshalJSON(value []byte) error {
	if len(value) == 0 {
		return errors.New("predicate is empty")
	}
	*raw = append((*raw)[:0], value...)
	return nil
}

type statementSubject struct {
	Name   string            `json:"name"`
	Digest map[string]string `json:"digest"`
}

func NewVerifier(policy TrustPolicy) (*Verifier, error) {
	if !validDigest(policy.Digest) {
		return nil, errors.New("qualification trust policy digest is required")
	}
	if len(policy.Signers) < 2 || policy.MinimumSignatures < 2 || policy.MinimumSignatures > len(policy.Signers) {
		return nil, errors.New("qualification trust policy requires a threshold of at least two configured signers")
	}
	if policy.MaxReceiptAge <= 0 || policy.MaxFutureSkew < 0 || policy.MaxFutureSkew > policy.MaxReceiptAge {
		return nil, errors.New("qualification receipt freshness policy is invalid")
	}
	trustedSigners := make(map[string]templateauthority.TrustedSigner, len(policy.Signers))
	roles := map[string]map[string]struct{}{SignerRoleRunner: {}, SignerRoleApprover: {}}
	identityRole := map[string]string{}
	keyFingerprints := map[string]string{}
	keyOwners := map[string]string{}
	identityOwners := map[string]string{}
	registerIndependentKey := func(keyID, identity string, publicKey any, owner string) error {
		if prior, duplicate := keyOwners[keyID]; duplicate {
			return fmt.Errorf("signing key id %q is reused by %s and %s", keyID, prior, owner)
		}
		if prior, duplicate := identityOwners[identity]; duplicate {
			return fmt.Errorf("signing identity %q is reused by %s and %s", identity, prior, owner)
		}
		fingerprint, err := publicKeyFingerprint(publicKey)
		if err != nil {
			return fmt.Errorf("%s public key is invalid: %w", owner, err)
		}
		if prior, duplicate := keyFingerprints[fingerprint]; duplicate {
			return fmt.Errorf("%s reuses public key %q", owner, prior)
		}
		keyOwners[keyID] = owner
		identityOwners[identity] = owner
		keyFingerprints[fingerprint] = owner
		return nil
	}
	for keyID, signer := range policy.Signers {
		if !validCanonicalString(keyID, 256) || !validCanonicalString(signer.Identity, 2048) {
			return nil, fmt.Errorf("qualification signer %q key id or identity is invalid", keyID)
		}
		if signer.Role != SignerRoleRunner && signer.Role != SignerRoleApprover {
			return nil, fmt.Errorf("qualification signer %q has unsupported role", keyID)
		}
		if signer.NotBefore.IsZero() || signer.NotAfter.IsZero() || !signer.NotAfter.After(signer.NotBefore) {
			return nil, fmt.Errorf("qualification signer %q has an invalid validity window", keyID)
		}
		if _, duplicateIdentity := identityRole[signer.Identity]; duplicateIdentity {
			return nil, fmt.Errorf("qualification identity %q cannot be assigned to more than one signing key", signer.Identity)
		}
		identityRole[signer.Identity] = signer.Role
		if err := registerIndependentKey(keyID, signer.Identity, signer.PublicKey, "qualification signer "+keyID); err != nil {
			return nil, err
		}
		roles[signer.Role][signer.Identity] = struct{}{}
		trustedSigners[keyID] = templateauthority.TrustedSigner{
			Algorithm: signer.Algorithm, PublicKey: signer.PublicKey, Identity: signer.Identity,
		}
	}
	if len(roles[SignerRoleRunner]) == 0 || len(roles[SignerRoleApprover]) == 0 {
		return nil, errors.New("qualification trust policy must contain independent runner and approver identities")
	}
	receiptVerifier, err := templateauthority.NewDSSEVerifier(templateauthority.DSSETrustPolicy{
		Keys: trustedSigners, AllowedPayloadTypes: []string{InTotoPayloadType},
		AllowedPredicateTypes: []string{QualificationPredicateTypeV2}, MinSignatures: policy.MinimumSignatures,
	})
	if err != nil {
		return nil, fmt.Errorf("configure qualification DSSE verifier: %w", err)
	}
	if len(policy.CredentialIssuers) == 0 {
		return nil, errors.New("qualification trust policy must contain credential issuers")
	}
	if len(policy.EncryptionRecipients) == 0 {
		return nil, errors.New("qualification trust policy must contain approved evidence encryption recipients")
	}
	for index, recipient := range policy.EncryptionRecipients {
		if !validCanonicalString(recipient.KeyResource, 2048) || !validCanonicalString(recipient.KeyVersion, 256) ||
			(index > 0 && (policy.EncryptionRecipients[index-1].KeyResource > recipient.KeyResource ||
				(policy.EncryptionRecipients[index-1].KeyResource == recipient.KeyResource && policy.EncryptionRecipients[index-1].KeyVersion >= recipient.KeyVersion))) {
			return nil, errors.New("qualification encryption recipients must be canonical, unique, and sorted")
		}
	}
	credentialAuthorities := make(map[string]credentialAuthority, len(policy.CredentialIssuers))
	for issuerID, issuer := range policy.CredentialIssuers {
		if issuerID != issuer.Issuer || !validCanonicalString(issuerID, 256) || len(issuer.Keys) == 0 ||
			issuer.MinimumSignatures < 1 || issuer.MinimumSignatures > len(issuer.Keys) {
			return nil, fmt.Errorf("credential issuer %q policy is invalid", issuerID)
		}
		issuerIdentities := map[string]struct{}{}
		validityCopy := make(map[string]AuthorityKeyValidity, len(issuer.Keys))
		for keyID, signer := range issuer.Keys {
			if !validCanonicalString(keyID, 256) || !validCanonicalString(signer.Identity, 2048) {
				return nil, fmt.Errorf("credential issuer %q key %q id or identity is invalid", issuerID, keyID)
			}
			if _, duplicate := issuerIdentities[signer.Identity]; duplicate {
				return nil, fmt.Errorf("credential issuer %q identity %q is assigned to multiple keys", issuerID, signer.Identity)
			}
			issuerIdentities[signer.Identity] = struct{}{}
			if err := registerIndependentKey(keyID, signer.Identity, signer.PublicKey, "credential issuer "+issuerID+"/"+keyID); err != nil {
				return nil, err
			}
			validity, exists := issuer.KeyValidity[keyID]
			if !exists || validity.NotBefore.IsZero() || !validity.NotAfter.After(validity.NotBefore) {
				return nil, fmt.Errorf("credential issuer %q key %q has no valid lifecycle window", issuerID, keyID)
			}
			validityCopy[keyID] = copyAuthorityKeyValidity(validity)
		}
		credentialVerifier, err := templateauthority.NewDSSEVerifier(templateauthority.DSSETrustPolicy{
			Keys: issuer.Keys, AllowedPayloadTypes: []string{InTotoPayloadType},
			AllowedPredicateTypes: []string{CredentialSetIssuancePredicateTypeV1, CredentialSetRevocationPredicateTypeV1},
			MinSignatures:         issuer.MinimumSignatures,
		})
		if err != nil {
			return nil, fmt.Errorf("configure credential issuer %q: %w", issuerID, err)
		}
		allowed := make(map[string]struct{}, len(issuer.AllowedIdentities))
		for _, identity := range issuer.AllowedIdentities {
			if !validCanonicalString(identity, 2048) {
				return nil, fmt.Errorf("credential issuer %q has invalid allowed identity", issuerID)
			}
			allowed[identity] = struct{}{}
		}
		if len(allowed) == 0 {
			return nil, fmt.Errorf("credential issuer %q has no allowed identities", issuerID)
		}
		if !sameIdentitySet(allowed, issuerIdentities) {
			return nil, fmt.Errorf("credential issuer %q allowed identities must exactly match its independent keys", issuerID)
		}
		credentialAuthorities[issuerID] = credentialAuthority{verifier: credentialVerifier, allowedIdentities: allowed, keyValidity: validityCopy}
	}
	encryptionTrust := policy.EncryptionAuthority
	if len(encryptionTrust.Keys) == 0 || encryptionTrust.MinimumSignatures < 1 ||
		encryptionTrust.MinimumSignatures > len(encryptionTrust.Keys) {
		return nil, errors.New("qualification trust policy must contain a threshold evidence encryption authority")
	}
	encryptionKeyIdentities := map[string]struct{}{}
	encryptionValidity := make(map[string]AuthorityKeyValidity, len(encryptionTrust.Keys))
	for keyID, signer := range encryptionTrust.Keys {
		if !validCanonicalString(keyID, 256) || !validCanonicalString(signer.Identity, 2048) {
			return nil, fmt.Errorf("encryption authority key %q id or identity is invalid", keyID)
		}
		if _, duplicate := encryptionKeyIdentities[signer.Identity]; duplicate {
			return nil, fmt.Errorf("encryption authority identity %q is assigned to multiple keys", signer.Identity)
		}
		encryptionKeyIdentities[signer.Identity] = struct{}{}
		if err := registerIndependentKey(keyID, signer.Identity, signer.PublicKey, "encryption authority "+keyID); err != nil {
			return nil, err
		}
		validity, exists := encryptionTrust.KeyValidity[keyID]
		if !exists || validity.NotBefore.IsZero() || !validity.NotAfter.After(validity.NotBefore) {
			return nil, fmt.Errorf("encryption authority key %q has no valid lifecycle window", keyID)
		}
		encryptionValidity[keyID] = copyAuthorityKeyValidity(validity)
	}
	encryptionVerifier, err := templateauthority.NewDSSEVerifier(templateauthority.DSSETrustPolicy{
		Keys: encryptionTrust.Keys, AllowedPayloadTypes: []string{InTotoPayloadType},
		AllowedPredicateTypes: []string{EncryptionPredicateTypeV1}, MinSignatures: encryptionTrust.MinimumSignatures,
	})
	if err != nil {
		return nil, fmt.Errorf("configure evidence encryption authority: %w", err)
	}
	encryptionIdentities := make(map[string]struct{}, len(encryptionTrust.AllowedIdentities))
	for _, identity := range encryptionTrust.AllowedIdentities {
		if !validCanonicalString(identity, 2048) {
			return nil, errors.New("encryption authority has an invalid allowed identity")
		}
		encryptionIdentities[identity] = struct{}{}
	}
	if len(encryptionIdentities) == 0 {
		return nil, errors.New("encryption authority has no allowed identities")
	}
	if !sameIdentitySet(encryptionIdentities, encryptionKeyIdentities) {
		return nil, errors.New("encryption authority allowed identities must exactly match its independent keys")
	}
	faultTrust := policy.FaultAuthority
	if len(faultTrust.Keys) == 0 || faultTrust.MinimumSignatures < 1 ||
		faultTrust.MinimumSignatures > len(faultTrust.Keys) ||
		!sortedUniqueStrings(faultTrust.AllowedIdentities, func(value string) bool { return validCanonicalString(value, 2048) }) {
		return nil, errors.New("qualification trust policy must contain a threshold Golden fault authority")
	}
	faultIdentities := make(map[string]struct{}, len(faultTrust.Keys))
	faultAllowed := make(map[string]struct{}, len(faultTrust.AllowedIdentities))
	faultSigners := make(map[string]goldenfault.SignerTrust, len(faultTrust.Keys))
	for _, identity := range faultTrust.AllowedIdentities {
		if !validCanonicalString(identity, 2048) {
			return nil, errors.New("Golden fault authority has an invalid allowed identity")
		}
		if _, duplicate := faultAllowed[identity]; duplicate {
			return nil, errors.New("Golden fault authority allowed identities must be unique")
		}
		faultAllowed[identity] = struct{}{}
	}
	for keyID, signer := range faultTrust.Keys {
		if !validCanonicalString(keyID, 256) || !validCanonicalString(signer.Identity, 2048) {
			return nil, fmt.Errorf("Golden fault authority key %q id or identity is invalid", keyID)
		}
		if _, duplicate := faultIdentities[signer.Identity]; duplicate {
			return nil, fmt.Errorf("Golden fault authority identity %q is assigned to multiple keys", signer.Identity)
		}
		validity, exists := faultTrust.KeyValidity[keyID]
		if !exists || !validAuthorityKeyLifecycle(validity) {
			return nil, fmt.Errorf("Golden fault authority key %q has no valid lifecycle window", keyID)
		}
		if err := registerIndependentKey(keyID, signer.Identity, signer.PublicKey, "Golden fault authority "+keyID); err != nil {
			return nil, err
		}
		faultIdentities[signer.Identity] = struct{}{}
		faultSigners[keyID] = goldenfault.SignerTrust{
			Algorithm: signer.Algorithm, PublicKey: signer.PublicKey, Identity: signer.Identity,
			Role: goldenfault.FaultOperatorRole, NotBefore: validity.NotBefore, NotAfter: validity.NotAfter,
			RevokedAt: validity.RevokedAt,
		}
	}
	if !sameIdentitySet(faultAllowed, faultIdentities) {
		return nil, errors.New("Golden fault authority allowed identities must exactly match its independent keys")
	}
	faultVerifier, err := goldenfault.NewVerifier(goldenfault.TrustPolicy{
		Signers: faultSigners, MinimumSignatures: faultTrust.MinimumSignatures,
	})
	if err != nil {
		return nil, fmt.Errorf("configure Golden fault authority verifier: %w", err)
	}
	attestorTrust := policy.FaultLedgerAttestor
	if len(attestorTrust.Keys) == 0 || attestorTrust.MinimumSignatures < 1 ||
		attestorTrust.MinimumSignatures > len(attestorTrust.Keys) ||
		!sortedUniqueStrings(attestorTrust.AllowedIdentities, func(value string) bool { return validCanonicalString(value, 2048) }) {
		return nil, errors.New("qualification trust policy must contain a threshold Golden fault ledger attestor")
	}
	attestorIdentities := make(map[string]struct{}, len(attestorTrust.Keys))
	attestorAllowed := make(map[string]struct{}, len(attestorTrust.AllowedIdentities))
	attestorValidity := make(map[string]AuthorityKeyValidity, len(attestorTrust.Keys))
	for _, identity := range attestorTrust.AllowedIdentities {
		if !validCanonicalString(identity, 2048) {
			return nil, errors.New("Golden fault ledger attestor has an invalid allowed identity")
		}
		if _, duplicate := attestorAllowed[identity]; duplicate {
			return nil, errors.New("Golden fault ledger attestor allowed identities must be unique")
		}
		attestorAllowed[identity] = struct{}{}
	}
	for keyID, signer := range attestorTrust.Keys {
		if !validCanonicalString(keyID, 256) || !validCanonicalString(signer.Identity, 2048) {
			return nil, fmt.Errorf("Golden fault ledger attestor key %q id or identity is invalid", keyID)
		}
		if _, duplicate := attestorIdentities[signer.Identity]; duplicate {
			return nil, fmt.Errorf("Golden fault ledger attestor identity %q is assigned to multiple keys", signer.Identity)
		}
		validity, exists := attestorTrust.KeyValidity[keyID]
		if !exists || !validAuthorityKeyLifecycle(validity) {
			return nil, fmt.Errorf("Golden fault ledger attestor key %q has no valid lifecycle window", keyID)
		}
		if err := registerIndependentKey(keyID, signer.Identity, signer.PublicKey, "Golden fault ledger attestor "+keyID); err != nil {
			return nil, err
		}
		attestorIdentities[signer.Identity] = struct{}{}
		attestorValidity[keyID] = copyAuthorityKeyValidity(validity)
	}
	if !sameIdentitySet(attestorAllowed, attestorIdentities) {
		return nil, errors.New("Golden fault ledger attestor allowed identities must exactly match its independent keys")
	}
	attestorVerifier, err := templateauthority.NewDSSEVerifier(templateauthority.DSSETrustPolicy{
		Keys: attestorTrust.Keys, AllowedPayloadTypes: []string{InTotoPayloadType},
		AllowedPredicateTypes: []string{GoldenFaultLedgerPredicateTypeV1},
		MinSignatures:         attestorTrust.MinimumSignatures,
	})
	if err != nil {
		return nil, fmt.Errorf("configure Golden fault ledger attestor: %w", err)
	}
	copiedPolicy := TrustPolicy{
		Digest: policy.Digest, Signers: make(map[string]SignerTrust, len(policy.Signers)),
		MinimumSignatures: policy.MinimumSignatures, MaxReceiptAge: policy.MaxReceiptAge,
		MaxFutureSkew:        policy.MaxFutureSkew,
		EncryptionRecipients: append([]EncryptionRecipient(nil), policy.EncryptionRecipients...),
	}
	for keyID, signer := range policy.Signers {
		copied := signer
		if signer.RevokedAt != nil {
			revokedAt := *signer.RevokedAt
			copied.RevokedAt = &revokedAt
		}
		copiedPolicy.Signers[keyID] = copied
	}
	return &Verifier{
		policy: copiedPolicy, receiptVerifier: receiptVerifier, credentialAuthorities: credentialAuthorities,
		encryptionAuthority: credentialAuthority{verifier: encryptionVerifier, allowedIdentities: encryptionIdentities, keyValidity: encryptionValidity},
		faultAuthority:      faultVerifier, faultAuthorityAllowed: faultAllowed,
		faultLedgerAttestor: credentialAuthority{verifier: attestorVerifier, allowedIdentities: attestorAllowed, keyValidity: attestorValidity},
		snapshotInspector:   requireReadOnlyMount,
	}, nil
}

func copyAuthorityKeyValidity(validity AuthorityKeyValidity) AuthorityKeyValidity {
	copy := validity
	if validity.RevokedAt != nil {
		revokedAt := *validity.RevokedAt
		copy.RevokedAt = &revokedAt
	}
	return copy
}

func sameIdentitySet(left, right map[string]struct{}) bool {
	if len(left) != len(right) {
		return false
	}
	for identity := range left {
		if _, exists := right[identity]; !exists {
			return false
		}
	}
	return true
}

func validAuthorityKeyLifecycle(validity AuthorityKeyValidity) bool {
	if validity.NotBefore.IsZero() || !validity.NotAfter.After(validity.NotBefore) {
		return false
	}
	return validity.RevokedAt == nil ||
		(!validity.RevokedAt.Before(validity.NotBefore) && validity.RevokedAt.Before(validity.NotAfter))
}

func validateAuthoritySignerValidity(authority credentialAuthority, verified *templateauthority.VerifiedDSSE, signedAt time.Time, label string) error {
	for _, signer := range verified.Signers {
		validity, exists := authority.keyValidity[signer.KeyID]
		if !exists || signedAt.Before(validity.NotBefore) || !signedAt.Before(validity.NotAfter) ||
			(validity.RevokedAt != nil && !signedAt.Before(*validity.RevokedAt)) {
			return fmt.Errorf("%s signer %q was not valid at the independently bound signing time", label, signer.KeyID)
		}
	}
	return nil
}

func (verifier *Verifier) Verify(receiptPath, indexPath, artifactRoot string, expected ExpectedPromotion) (VerifiedPromotion, error) {
	if verifier == nil || verifier.receiptVerifier == nil {
		return VerifiedPromotion{}, errors.New("qualification verifier is not configured")
	}
	for label, candidate := range map[string]string{"receipt": receiptPath, "artifact index": indexPath, "artifact root": artifactRoot} {
		if !filepath.IsAbs(candidate) || filepath.Clean(candidate) != candidate {
			return VerifiedPromotion{}, fmt.Errorf("%s path must be absolute and normalized", label)
		}
	}
	if err := validateExpectedPromotion(expected); err != nil {
		return VerifiedPromotion{}, err
	}
	if artifactRoot != expected.ArtifactRoot {
		return VerifiedPromotion{}, errors.New("artifact root does not match the server-owned immutable snapshot authority")
	}
	if !pathWithinRoot(expected.EvidenceSnapshotRoot, receiptPath) || !pathWithinRoot(expected.EvidenceSnapshotRoot, indexPath) ||
		!pathWithinRoot(expected.EvidenceSnapshotRoot, artifactRoot) {
		return VerifiedPromotion{}, errors.New("promotion evidence is outside the server-owned immutable evidence snapshot")
	}
	if receiptPath == indexPath || pathWithinRoot(artifactRoot, receiptPath) || pathWithinRoot(artifactRoot, indexPath) {
		return VerifiedPromotion{}, errors.New("receipt and artifact index must be distinct files outside the artifact directory")
	}
	for _, requiredPath := range []struct {
		path      string
		directory bool
	}{
		{expected.EvidenceSnapshotRoot, true}, {artifactRoot, true}, {receiptPath, false}, {indexPath, false},
	} {
		if err := verifyRealEvidencePath(requiredPath.path, requiredPath.directory); err != nil {
			return VerifiedPromotion{}, err
		}
	}
	if verifier.snapshotInspector == nil {
		return VerifiedPromotion{}, errors.New("immutable artifact snapshot inspector is not configured")
	}
	if err := verifier.snapshotInspector(expected.EvidenceSnapshotRoot); err != nil {
		return VerifiedPromotion{}, fmt.Errorf("artifact snapshot is not immutable: %w", err)
	}
	if err := verifier.verifyImmutableSnapshotTree(expected.EvidenceSnapshotRoot); err != nil {
		return VerifiedPromotion{}, err
	}
	indexBytes, index, err := readArtifactIndex(indexPath)
	if err != nil {
		return VerifiedPromotion{}, err
	}
	indexDigest := sha256Digest(indexBytes)
	if indexDigest != expected.ArtifactIndexDigest {
		return VerifiedPromotion{}, errors.New("artifact index does not match the server-owned snapshot digest")
	}
	if err := validateArtifactIndex(index, expected); err != nil {
		return VerifiedPromotion{}, err
	}
	if err := verifier.verifyExactEvidenceFileClosure(expected.EvidenceSnapshotRoot, receiptPath, indexPath, artifactRoot, index); err != nil {
		return VerifiedPromotion{}, err
	}
	receiptBytes, err := readBoundedRegularFile(receiptPath, maxReceiptBytes, true)
	if err != nil {
		return VerifiedPromotion{}, fmt.Errorf("read signed qualification receipt: %w", err)
	}
	receiptBundleDigest := sha256Digest(receiptBytes)
	if receiptBundleDigest != expected.ReceiptBundleDigest {
		return VerifiedPromotion{}, errors.New("qualification receipt bundle does not match the server-owned authority digest")
	}
	verifiedDSSE, err := verifier.receiptVerifier.Verify(receiptBytes, templateauthority.ExpectedSubject{
		Name: "worksflow-qualification-artifacts/" + expected.RunID, SHA256Digest: indexDigest,
	})
	if err != nil {
		return VerifiedPromotion{}, fmt.Errorf("verify signed qualification receipt: %w", err)
	}
	statement, err := parseStatement(verifiedDSSE.Payload, QualificationPredicateTypeV2)
	if err != nil {
		return VerifiedPromotion{}, err
	}
	var receipt QualificationReceipt
	if err := requireExactShape(statement.Predicate, receiptShape()); err != nil {
		return VerifiedPromotion{}, fmt.Errorf("validate qualification receipt predicate shape: %w", err)
	}
	if err := decodeStrictJSON(statement.Predicate, &receipt); err != nil {
		return VerifiedPromotion{}, fmt.Errorf("decode qualification receipt predicate: %w", err)
	}
	issuedAt, err := verifier.validateReceiptAndSigners(receipt, verifiedDSSE, indexDigest, len(index.Artifacts), expected)
	if err != nil {
		return VerifiedPromotion{}, err
	}
	artifacts, err := verifyArtifactDirectory(artifactRoot, index, expected)
	if err != nil {
		return VerifiedPromotion{}, err
	}
	if receipt.ArtifactIndex.RestrictedEncryptedCount != artifacts.restrictedEncryptedCount || artifacts.traceCount == 0 || artifacts.videoCount == 0 {
		return VerifiedPromotion{}, errors.New("receipt does not close at least one encrypted trace and video artifact")
	}
	goldenFixture, err := verifyGoldenRuntimeDocuments(artifactRoot, receipt, expected, artifacts)
	if err != nil {
		return VerifiedPromotion{}, err
	}
	faultEvidence, err := verifier.verifyGoldenFaultEvidence(artifactRoot, receipt, expected, artifacts, goldenFixture)
	if err != nil {
		return VerifiedPromotion{}, err
	}
	if err := verifier.verifyEncryptionRecipients(artifacts); err != nil {
		return VerifiedPromotion{}, err
	}
	encryptionIdentities, err := verifier.verifyEncryptionAttestation(artifactRoot, index, receipt, expected, artifacts)
	if err != nil {
		return VerifiedPromotion{}, err
	}
	if err := verifySuiteResults(receipt, expected, artifacts); err != nil {
		return VerifiedPromotion{}, err
	}
	if err := verifyPlaywrightResult(artifactRoot, receipt, expected, artifacts); err != nil {
		return VerifiedPromotion{}, err
	}
	if err := verifyWriterDrainProof(artifactRoot, receipt, expected, artifacts); err != nil {
		return VerifiedPromotion{}, err
	}
	credentialVerification, err := verifier.verifyCredentialSet(artifactRoot, receipt, expected, artifacts)
	if err != nil {
		return VerifiedPromotion{}, err
	}
	closingArtifacts, err := verifyArtifactDirectory(artifactRoot, index, expected)
	if err != nil || closingArtifacts.restrictedEncryptedCount != artifacts.restrictedEncryptedCount ||
		closingArtifacts.traceCount != artifacts.traceCount || closingArtifacts.videoCount != artifacts.videoCount {
		return VerifiedPromotion{}, errors.New("artifact closure changed while qualification evidence was verified")
	}
	if err := verifier.verifyImmutableSnapshotTree(expected.EvidenceSnapshotRoot); err != nil {
		return VerifiedPromotion{}, err
	}
	if err := verifier.verifyExactEvidenceFileClosure(expected.EvidenceSnapshotRoot, receiptPath, indexPath, artifactRoot, index); err != nil {
		return VerifiedPromotion{}, err
	}
	return VerifiedPromotion{
		Scope: ExternalQualificationScope, PromotionTarget: receipt.PromotionTarget,
		AuthorityNonce: receipt.AuthorityNonce, AuthorityExpiresAt: receipt.AuthorityExpiresAt,
		PromotionAuthorityDigest: expected.PromotionAuthorityDigest, SingleUseConsumption: SingleUseConsumptionPolicy,
		GoldenRuntime: receipt.GoldenRuntime,
		CredentialSet: credentialVerification.binding,
		RunID:         receipt.RunID, PlanDigest: receipt.PlanDigest, ArtifactIndexDigest: indexDigest,
		ReceiptPayloadDigest: verifiedDSSE.PayloadDigest, ReceiptBundleDigest: receiptBundleDigest,
		SignerIdentities:                     append([]string(nil), verifiedDSSE.SignerIdentities...),
		CredentialIssuanceSignerIdentities:   credentialVerification.issuanceSignerIdentities,
		CredentialRevocationSignerIdentities: credentialVerification.revocationSignerIdentities,
		EncryptionSignerIdentities:           encryptionIdentities,
		FaultAuthoritySignerIdentities:       faultEvidence.authoritySignerIdentities,
		FaultLedgerAttestationDigest:         faultEvidence.attestationDigest,
		FaultLedgerAttestorSignerIdentities:  faultEvidence.attestorSignerIdentities,
		IssuedAt:                             issuedAt.Format(canonicalTimeLayout), Decision: receipt.Decision,
	}, nil
}

func (verifier *Verifier) verifyImmutableSnapshotTree(root string) error {
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || resolved != root {
		return errors.New("evidence snapshot root path must be real and contain no symlink components")
	}
	entries := 0
	directories := 0
	return filepath.WalkDir(root, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("evidence snapshot contains symlink %q", current)
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("evidence snapshot contains non-regular path %q", current)
		}
		if current != root {
			entries++
			relative, relErr := filepath.Rel(root, current)
			if relErr != nil || len(filepath.ToSlash(relative)) > maxEvidenceSnapshotPathBytes || entries > maxEvidenceSnapshotEntries {
				return errors.New("evidence snapshot exceeds its path or entry limit")
			}
			if info.IsDir() {
				directories++
				if directories > maxEvidenceSnapshotDirectories {
					return errors.New("evidence snapshot exceeds its directory limit")
				}
			}
		}
		if err := verifier.snapshotInspector(current); err != nil {
			return fmt.Errorf("artifact snapshot path %q is not immutable: %w", current, err)
		}
		return nil
	})
}

func verifyRealEvidencePath(candidate string, directory bool) error {
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil || resolved != candidate {
		return fmt.Errorf("evidence path %q must contain no symlink components", candidate)
	}
	info, err := os.Lstat(candidate)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || (directory && !info.IsDir()) || (!directory && !info.Mode().IsRegular()) {
		return fmt.Errorf("evidence path %q has the wrong filesystem type", candidate)
	}
	return nil
}

func (verifier *Verifier) verifyExactEvidenceFileClosure(root, receiptPath, indexPath, artifactRoot string, index ArtifactIndex) error {
	allowed := map[string]struct{}{receiptPath: {}, indexPath: {}}
	for _, descriptor := range index.Artifacts {
		absolute := filepath.Join(artifactRoot, filepath.FromSlash(descriptor.Path))
		if !pathWithinRoot(artifactRoot, absolute) {
			return fmt.Errorf("artifact %q escapes the evidence artifact directory", descriptor.ID)
		}
		allowed[absolute] = struct{}{}
	}
	seen := make(map[string]struct{}, len(allowed))
	entries := 0
	directories := 0
	err := filepath.WalkDir(root, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("evidence snapshot contains symlink %q", current)
		}
		if current != root {
			entries++
			relative, relErr := filepath.Rel(root, current)
			if relErr != nil || len(filepath.ToSlash(relative)) > maxEvidenceSnapshotPathBytes || entries > maxEvidenceSnapshotEntries {
				return errors.New("evidence snapshot exceeds its path or entry limit")
			}
		}
		if info.IsDir() {
			if current != root {
				directories++
				if directories > maxEvidenceSnapshotDirectories {
					return errors.New("evidence snapshot exceeds its directory limit")
				}
			}
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("evidence snapshot contains non-regular path %q", current)
		}
		if _, exists := allowed[current]; !exists {
			relative, _ := filepath.Rel(root, current)
			return fmt.Errorf("evidence snapshot contains unlisted file %q", filepath.ToSlash(relative))
		}
		seen[current] = struct{}{}
		return nil
	})
	if err != nil {
		return err
	}
	if len(seen) != len(allowed) {
		return errors.New("evidence snapshot does not contain the exact receipt, index, and artifact file closure")
	}
	return nil
}

func requireReadOnlyMount(root string) error {
	var filesystem unix.Statfs_t
	if err := unix.Statfs(root, &filesystem); err != nil {
		return err
	}
	if filesystem.Flags&unix.ST_RDONLY == 0 {
		return errors.New("snapshot filesystem is writable")
	}
	if filesystem.Type != int64(unix.SQUASHFS_MAGIC) && filesystem.Type != int64(unix.EROFS_SUPER_MAGIC_V1) {
		return errors.New("snapshot must use an intrinsically immutable squashfs or EROFS filesystem, not a writable filesystem through a read-only alias")
	}
	return nil
}

func (verifier *Verifier) verifyEncryptionRecipients(artifacts verifiedArtifactSet) error {
	allowed := make(map[string]struct{}, len(verifier.policy.EncryptionRecipients))
	for _, recipient := range verifier.policy.EncryptionRecipients {
		allowed[recipient.KeyResource+"\x00"+recipient.KeyVersion] = struct{}{}
	}
	for _, artifact := range artifacts.byID {
		if artifact.Encryption == nil {
			continue
		}
		if _, trusted := allowed[artifact.Encryption.KeyResource+"\x00"+artifact.Encryption.KeyVersion]; !trusted {
			return fmt.Errorf("artifact %q uses an encryption recipient absent from the server-owned trust policy", artifact.ID)
		}
	}
	return nil
}

func (verifier *Verifier) validateReceiptAndSigners(receipt QualificationReceipt, verified *templateauthority.VerifiedDSSE, indexDigest string, artifactCount int, expected ExpectedPromotion) (time.Time, error) {
	if receipt.SchemaVersion != ReceiptSchemaV2 || receipt.Scope != ExternalQualificationScope || receipt.Decision != "qualified" {
		return time.Time{}, errors.New("qualification receipt predicate is not a qualified v2 decision")
	}
	if receipt.PromotionTarget != expected.PromotionTarget || receipt.AuthorityNonce != expected.AuthorityNonce ||
		receipt.AuthorityExpiresAt != expected.AuthorityExpiresAt {
		return time.Time{}, errors.New("qualification receipt does not bind the server-owned promotion target, nonce, and expiry")
	}
	if receipt.RunID != expected.RunID || receipt.PlanDigest != expected.PlanDigest ||
		receipt.PrePromotionManifestDigest != expected.PrePromotionManifestDigest || receipt.TrustPolicyDigest != verifier.policy.Digest ||
		receipt.SourcePolicyAttestationDigest != expected.SourcePolicyAttestationDigest {
		return time.Time{}, errors.New("qualification receipt run, plan, manifest, or trust binding does not match promotion inputs")
	}
	if receipt.Source != expected.Source || receipt.TemplateRelease != expected.TemplateRelease || receipt.GoldenRuntime != expected.GoldenRuntime {
		return time.Time{}, errors.New("qualification receipt source, TemplateRelease, or Golden runtime binding does not match promotion inputs")
	}
	credentialSetAuthority := CredentialSetAuthorityBinding{
		Issuer: receipt.CredentialSet.Issuer, Audience: receipt.CredentialSet.Audience,
		SetHandleHash: receipt.CredentialSet.SetHandleHash, MemberBindingsDigest: receipt.CredentialSet.MemberBindingsDigest,
		MemberCount: receipt.CredentialSet.MemberCount,
	}
	if credentialSetAuthority != expected.CredentialSet || validateCredentialSetAuthorityBinding(credentialSetAuthority) != nil ||
		!validStableID(receipt.CredentialSet.Issuance.ArtifactID) || !validDigest(receipt.CredentialSet.Issuance.PayloadDigest) ||
		!validStableID(receipt.CredentialSet.Revocation.ArtifactID) || !validDigest(receipt.CredentialSet.Revocation.PayloadDigest) ||
		receipt.CredentialSet.Issuance.ArtifactID == receipt.CredentialSet.Revocation.ArtifactID ||
		receipt.CredentialSet.Issuance.PayloadDigest == receipt.CredentialSet.Revocation.PayloadDigest {
		return time.Time{}, errors.New("qualification receipt credential-set binding does not match the root-owned atomic set")
	}
	if receipt.ArtifactIndex.Digest != indexDigest || receipt.ArtifactIndex.Count != artifactCount {
		return time.Time{}, errors.New("qualification receipt does not bind the exact artifact index digest and count")
	}
	if receipt.Constructor.CompilerVersion != CompilerVersionV7 || receipt.Constructor.BuildContractHash != expected.BuildContractHash ||
		receipt.Constructor.WriterDrain.FromVersion != CompilerVersionV6 ||
		receipt.Constructor.WriterDrain.ToVersion != CompilerVersionV7 ||
		receipt.Constructor.WriterDrain.Status != "drained" ||
		receipt.Constructor.WriterDrain.ActiveWriters != 0 || receipt.Constructor.WriterDrain.InFlightMutations != 0 ||
		receipt.Constructor.WriterDrain.EvidenceArtifactID != expected.WriterDrainEvidenceArtifactID {
		return time.Time{}, errors.New("qualification receipt does not prove the exact v6-to-v7 writer drain and v7 BuildContract")
	}
	if !validDigest(receipt.Constructor.BuildContractHash) {
		return time.Time{}, errors.New("qualification receipt BuildContract hash is invalid")
	}
	startedAt, err := parseCanonicalTime(receipt.StartedAt, "receipt.startedAt")
	if err != nil {
		return time.Time{}, err
	}
	completedAt, err := parseCanonicalTime(receipt.CompletedAt, "receipt.completedAt")
	if err != nil {
		return time.Time{}, err
	}
	issuedAt, err := parseCanonicalTime(receipt.IssuedAt, "receipt.issuedAt")
	if err != nil {
		return time.Time{}, err
	}
	drainAt, err := parseCanonicalTime(receipt.Constructor.WriterDrain.CompletedAt, "receipt.constructor.writerDrain.completedAt")
	if err != nil || !drainAt.Before(startedAt) || !completedAt.After(startedAt) || !issuedAt.After(completedAt) {
		return time.Time{}, errors.New("writer drain, qualification run, and receipt chronology is invalid")
	}
	if receipt.IssuedAt != expected.TrustedReceiptIssuedAt {
		return time.Time{}, errors.New("receipt issuance time does not match the independently trusted promotion authority time")
	}
	authorityExpiresAt, _ := parseCanonicalTime(expected.AuthorityExpiresAt, "expected.authorityExpiresAt")
	if !issuedAt.Before(authorityExpiresAt) || !expected.VerifiedAt.UTC().Before(authorityExpiresAt) {
		return time.Time{}, errors.New("qualification receipt or verification occurred after promotion authority expiry")
	}
	now := expected.VerifiedAt.UTC()
	if issuedAt.After(now.Add(verifier.policy.MaxFutureSkew)) || issuedAt.Before(now.Add(-verifier.policy.MaxReceiptAge)) {
		return time.Time{}, errors.New("qualification receipt is outside its trusted freshness window")
	}
	roles := map[string]string{}
	for _, signer := range verified.Signers {
		trusted, exists := verifier.policy.Signers[signer.KeyID]
		if !exists || trusted.Identity != signer.Identity || issuedAt.Before(trusted.NotBefore) || !issuedAt.Before(trusted.NotAfter) ||
			(trusted.RevokedAt != nil && !issuedAt.Before(*trusted.RevokedAt)) {
			return time.Time{}, fmt.Errorf("qualification signer %q was not valid at receipt issuance", signer.KeyID)
		}
		if previous, exists := roles[trusted.Role]; exists && previous != trusted.Identity {
			continue
		}
		roles[trusted.Role] = trusted.Identity
	}
	runner, hasRunner := roles[SignerRoleRunner]
	approver, hasApprover := roles[SignerRoleApprover]
	if !hasRunner || !hasApprover || runner == approver {
		return time.Time{}, errors.New("qualification receipt lacks independent runner and approver signatures")
	}
	return issuedAt, nil
}

func verifySuiteResults(receipt QualificationReceipt, expected ExpectedPromotion, artifacts verifiedArtifactSet) error {
	if len(receipt.Suites) != len(expected.Suites) {
		return errors.New("qualification receipt suite set does not match the external plan")
	}
	for index, suite := range receipt.Suites {
		expectedSuite := expected.Suites[index]
		if suite.ID != expectedSuite.ID || suite.Result != "passed" ||
			suite.TestInventoryDigest != expected.TestInventoryDigest || !validDigest(suite.TestInventoryDigest) ||
			!equalStrings(suite.RequirementIDs, expectedSuite.RequirementIDs) ||
			!equalStrings(suite.ArtifactIDs, expectedSuite.RequiredArtifacts) {
			return fmt.Errorf("qualification receipt suite %q does not exactly match its plan, inventory, and required artifacts", suite.ID)
		}
		for _, artifactID := range suite.ArtifactIDs {
			artifact, exists := artifacts.byID[artifactID]
			if !exists || !containsString(artifact.SuiteIDs, suite.ID) {
				return fmt.Errorf("qualification receipt suite %q references missing artifact %q", suite.ID, artifactID)
			}
		}
	}
	if receipt.Totals.Discovered <= 0 || receipt.Totals.Passed != receipt.Totals.Discovered ||
		receipt.Totals.Failed != 0 || receipt.Totals.Skipped != 0 || receipt.Totals.Flaky != 0 ||
		receipt.Totals.Retried != 0 || receipt.Totals.Mocked != 0 {
		return errors.New("qualification receipt totals are not zero-failure, zero-skip, zero-retry, zero-flaky, and zero-mock")
	}
	return nil
}

func verifyPlaywrightResult(root string, receipt QualificationReceipt, expected ExpectedPromotion, artifacts verifiedArtifactSet) error {
	var descriptor *ArtifactDescriptor
	for _, artifact := range artifacts.byID {
		if artifact.Type == ArtifactTypePlaywrightResults {
			if descriptor != nil {
				return errors.New("artifact index contains multiple Playwright qualification results")
			}
			copy := artifact
			descriptor = &copy
		}
	}
	if descriptor == nil || descriptor.Classification != ClassificationDistributable {
		return errors.New("artifact index must contain one distributable Playwright qualification result")
	}
	encoded, err := readVerifiedArtifact(root, *descriptor, maxIndexBytes)
	if err != nil {
		return err
	}
	var result PlaywrightQualificationResult
	if err := requireExactShape(encoded, playwrightResultShape()); err != nil {
		return fmt.Errorf("validate Playwright qualification result shape: %w", err)
	}
	if err := decodeStrictJSON(encoded, &result); err != nil {
		return fmt.Errorf("decode Playwright qualification result: %w", err)
	}
	if result.SchemaVersion != PlaywrightResultSchemaV1 || result.RunID != expected.RunID ||
		result.TestInventoryDigest != expected.TestInventoryDigest || !result.Config.ForbidOnly ||
		result.Config.Retries != 0 || result.Config.Workers != 1 {
		return errors.New("Playwright qualification result does not bind the strict run, inventory, and configuration")
	}
	expectedCases := make([]ExpectedTestCase, 0)
	for _, testCase := range expected.TestCases {
		if testCase.Mode == "qualification" {
			expectedCases = append(expectedCases, testCase)
		}
	}
	if len(expectedCases) == 0 || len(result.Tests) != len(expectedCases) {
		return errors.New("Playwright qualification result does not contain the exact discovered inventory")
	}
	for index, test := range result.Tests {
		expectedCase := expectedCases[index]
		if test.CaseID != expectedCase.CaseID || test.SuiteID != expectedCase.SuiteID ||
			!equalStrings(test.RequirementIDs, expectedCase.RequirementIDs) ||
			!equalStrings(test.ContractCriterionIDs, expectedCase.ContractCriterionIDs) || test.Status != "passed" ||
			test.Retry != 0 || test.Flaky || test.Mocked {
			return fmt.Errorf("Playwright case %q is missing, reordered, skipped, retried, flaky, mocked, or failed", expectedCase.CaseID)
		}
	}
	exactTotals := QualificationTotals{Discovered: len(result.Tests), Passed: len(result.Tests)}
	if result.Totals != exactTotals || receipt.Totals != exactTotals {
		return errors.New("Playwright totals do not exactly match the discovered passing cases")
	}
	return nil
}

func verifyWriterDrainProof(root string, receipt QualificationReceipt, expected ExpectedPromotion, artifacts verifiedArtifactSet) error {
	descriptor, exists := artifacts.byID[expected.WriterDrainEvidenceArtifactID]
	if !exists || descriptor.Type != ArtifactTypeWriterDrain || descriptor.Classification != ClassificationDistributable {
		return errors.New("v7 writer-drain evidence artifact is missing or has the wrong type")
	}
	encoded, err := readVerifiedArtifact(root, descriptor, maxIndexBytes)
	if err != nil {
		return err
	}
	var proof WriterDrainProof
	if err := requireExactShape(encoded, writerDrainProofShape()); err != nil {
		return fmt.Errorf("validate writer-drain proof shape: %w", err)
	}
	if err := decodeStrictJSON(encoded, &proof); err != nil {
		return fmt.Errorf("decode writer-drain proof: %w", err)
	}
	drain := receipt.Constructor.WriterDrain
	if proof.SchemaVersion != WriterDrainProofSchemaV1 || proof.PlanDigest != expected.PlanDigest ||
		proof.TemplateRelease != expected.TemplateRelease || proof.FromVersion != CompilerVersionV6 ||
		proof.ToVersion != CompilerVersionV7 || proof.Status != "drained" || proof.ActiveWriters != 0 ||
		proof.InFlightMutations != 0 || proof.CompletedAt != drain.CompletedAt {
		return errors.New("writer-drain proof does not exactly bind the plan, TemplateRelease, versions, zero writers, and completion")
	}
	return nil
}

type credentialSetVerification struct {
	binding                    VerifiedCredentialSetBinding
	issuanceSignerIdentities   []string
	revocationSignerIdentities []string
}

func (verifier *Verifier) verifyCredentialSet(root string, receipt QualificationReceipt, expected ExpectedPromotion, artifacts verifiedArtifactSet) (credentialSetVerification, error) {
	binding := receipt.CredentialSet
	authorityBinding := CredentialSetAuthorityBinding{
		Issuer: binding.Issuer, Audience: binding.Audience, SetHandleHash: binding.SetHandleHash,
		MemberBindingsDigest: binding.MemberBindingsDigest, MemberCount: binding.MemberCount,
	}
	if authorityBinding != expected.CredentialSet || validateCredentialSetAuthorityBinding(authorityBinding) != nil {
		return credentialSetVerification{}, errors.New("credential-set binding does not match the trusted atomic set")
	}
	if binding.Issuance.ArtifactID == binding.Revocation.ArtifactID || binding.Issuance.PayloadDigest == binding.Revocation.PayloadDigest ||
		!validStableID(binding.Issuance.ArtifactID) || !validDigest(binding.Issuance.PayloadDigest) ||
		!validStableID(binding.Revocation.ArtifactID) || !validDigest(binding.Revocation.PayloadDigest) {
		return credentialSetVerification{}, errors.New("credential-set issuance and revocation artifact bindings must be distinct and canonical")
	}
	issuanceDescriptor, issuanceExists := artifacts.byID[binding.Issuance.ArtifactID]
	revocationDescriptor, revocationExists := artifacts.byID[binding.Revocation.ArtifactID]
	if !issuanceExists || issuanceDescriptor.Type != ArtifactTypeCredentialSetIssuance ||
		issuanceDescriptor.Classification != ClassificationDistributable || issuanceDescriptor.MediaType != "application/json" {
		return credentialSetVerification{}, errors.New("signed credential-set issuance artifact is missing or has the wrong type")
	}
	if !revocationExists || revocationDescriptor.Type != ArtifactTypeCredentialSetRevocation ||
		revocationDescriptor.Classification != ClassificationDistributable || revocationDescriptor.MediaType != "application/json" {
		return credentialSetVerification{}, errors.New("signed credential-set revocation artifact is missing or has the wrong type")
	}
	issuanceCount := 0
	revocationCount := 0
	for _, descriptor := range artifacts.byID {
		switch descriptor.Type {
		case ArtifactTypeCredentialSetIssuance:
			issuanceCount++
		case ArtifactTypeCredentialSetRevocation:
			revocationCount++
		}
	}
	if issuanceCount != 1 || revocationCount != 1 {
		return credentialSetVerification{}, errors.New("artifact index must contain exactly one credential-set issuance and revocation")
	}
	authority, trusted := verifier.credentialAuthorities[binding.Issuer]
	if !trusted {
		return credentialSetVerification{}, errors.New("credential-set issuer is not trusted")
	}
	issuance, issuanceVerified, err := verifier.verifyCredentialSetIssuance(root, issuanceDescriptor, binding, expected, authority)
	if err != nil {
		return credentialSetVerification{}, err
	}
	revocation, revocationVerified, err := verifier.verifyCredentialSetRevocation(root, revocationDescriptor, binding, expected, authority)
	if err != nil {
		return credentialSetVerification{}, err
	}
	if !equalCredentialSetMembers(issuance.Members, revocation.Members) {
		return credentialSetVerification{}, errors.New("credential-set issuance and revocation do not contain the identical sorted member list")
	}
	credentialIssued, _ := parseCanonicalTime(binding.IssuedAt, "credentialSet.issuedAt")
	credentialExpires, _ := parseCanonicalTime(binding.ExpiresAt, "credentialSet.expiresAt")
	credentialRevoked, _ := parseCanonicalTime(binding.RevokedAt, "credentialSet.revokedAt")
	runStarted, _ := parseCanonicalTime(receipt.StartedAt, "receipt.startedAt")
	runCompleted, _ := parseCanonicalTime(receipt.CompletedAt, "receipt.completedAt")
	receiptIssued, _ := parseCanonicalTime(receipt.IssuedAt, "receipt.issuedAt")
	drainCompleted, _ := parseCanonicalTime(receipt.Constructor.WriterDrain.CompletedAt, "receipt.constructor.writerDrain.completedAt")
	if !drainCompleted.Before(credentialIssued) || !credentialIssued.Before(runStarted) ||
		!credentialExpires.After(runCompleted) || credentialExpires.Sub(credentialIssued) > 30*time.Minute ||
		!credentialExpires.After(credentialIssued) ||
		!credentialRevoked.After(runCompleted) || !credentialRevoked.Before(credentialExpires) || !receiptIssued.After(credentialRevoked) {
		return credentialSetVerification{}, errors.New("credential-set issuance, qualification, expiry, revocation, and receipt chronology is invalid")
	}
	return credentialSetVerification{
		binding: VerifiedCredentialSetBinding{
			Issuer: binding.Issuer, Audience: binding.Audience, SetHandleHash: binding.SetHandleHash,
			MemberBindingsDigest: binding.MemberBindingsDigest, MemberCount: binding.MemberCount,
			IssuanceArtifactID: binding.Issuance.ArtifactID, IssuancePayloadDigest: binding.Issuance.PayloadDigest,
			RevocationArtifactID: binding.Revocation.ArtifactID, RevocationPayloadDigest: binding.Revocation.PayloadDigest,
		},
		issuanceSignerIdentities:   append([]string(nil), issuanceVerified.SignerIdentities...),
		revocationSignerIdentities: append([]string(nil), revocationVerified.SignerIdentities...),
	}, nil
}

func (verifier *Verifier) verifyCredentialSetIssuance(
	root string,
	descriptor ArtifactDescriptor,
	binding CredentialSetBinding,
	expected ExpectedPromotion,
	authority credentialAuthority,
) (CredentialSetIssuance, *templateauthority.VerifiedDSSE, error) {
	envelope, err := readVerifiedArtifact(root, descriptor, maxReceiptBytes)
	if err != nil {
		return CredentialSetIssuance{}, nil, err
	}
	verified, err := authority.verifier.Verify(envelope, credentialSetExpectedSubject(binding.SetHandleHash))
	if err != nil {
		return CredentialSetIssuance{}, nil, fmt.Errorf("verify credential-set issuance DSSE: %w", err)
	}
	if verified.PayloadDigest != binding.Issuance.PayloadDigest {
		return CredentialSetIssuance{}, nil, errors.New("credential-set issuance payload digest does not match the qualification receipt")
	}
	if err := validateCredentialSignerIdentities(authority, verified, "issuance"); err != nil {
		return CredentialSetIssuance{}, nil, err
	}
	statement, err := parseStatement(verified.Payload, CredentialSetIssuancePredicateTypeV1)
	if err != nil {
		return CredentialSetIssuance{}, nil, err
	}
	if err := requireExactShape(statement.Predicate, credentialSetIssuanceShape()); err != nil {
		return CredentialSetIssuance{}, nil, fmt.Errorf("validate credential-set issuance predicate shape: %w", err)
	}
	var issuance CredentialSetIssuance
	if err := decodeStrictJSON(statement.Predicate, &issuance); err != nil {
		return CredentialSetIssuance{}, nil, fmt.Errorf("decode credential-set issuance predicate: %w", err)
	}
	if issuance.SchemaVersion != CredentialSetIssuanceSchemaV1 || issuance.RunID != expected.RunID ||
		issuance.FixtureID != expected.GoldenRuntime.FixtureID || issuance.Issuer != binding.Issuer ||
		issuance.Audience != binding.Audience || issuance.SetHandleHash != binding.SetHandleHash ||
		issuance.MemberBindingsDigest != binding.MemberBindingsDigest || issuance.MemberCount != binding.MemberCount ||
		issuance.Status != "issued" || issuance.IssuedAt != binding.IssuedAt || issuance.ExpiresAt != binding.ExpiresAt {
		return CredentialSetIssuance{}, nil, errors.New("credential-set issuance predicate does not exactly match the receipt and Golden fixture")
	}
	if err := validateCredentialSetMembers(issuance.Members, binding.MemberBindingsDigest, binding.MemberCount, binding.SetHandleHash); err != nil {
		return CredentialSetIssuance{}, nil, err
	}
	issuedAt, err := parseCanonicalTime(issuance.IssuedAt, "credentialSetIssuance.issuedAt")
	if err != nil {
		return CredentialSetIssuance{}, nil, err
	}
	if _, err := parseCanonicalTime(issuance.ExpiresAt, "credentialSetIssuance.expiresAt"); err != nil {
		return CredentialSetIssuance{}, nil, err
	}
	if err := validateAuthoritySignerValidity(authority, verified, issuedAt, "credential-set issuance"); err != nil {
		return CredentialSetIssuance{}, nil, err
	}
	return issuance, verified, nil
}

func (verifier *Verifier) verifyCredentialSetRevocation(
	root string,
	descriptor ArtifactDescriptor,
	binding CredentialSetBinding,
	expected ExpectedPromotion,
	authority credentialAuthority,
) (CredentialSetRevocation, *templateauthority.VerifiedDSSE, error) {
	envelope, err := readVerifiedArtifact(root, descriptor, maxReceiptBytes)
	if err != nil {
		return CredentialSetRevocation{}, nil, err
	}
	verified, err := authority.verifier.Verify(envelope, credentialSetExpectedSubject(binding.SetHandleHash))
	if err != nil {
		return CredentialSetRevocation{}, nil, fmt.Errorf("verify credential-set revocation DSSE: %w", err)
	}
	if verified.PayloadDigest != binding.Revocation.PayloadDigest {
		return CredentialSetRevocation{}, nil, errors.New("credential-set revocation payload digest does not match the qualification receipt")
	}
	if err := validateCredentialSignerIdentities(authority, verified, "revocation"); err != nil {
		return CredentialSetRevocation{}, nil, err
	}
	statement, err := parseStatement(verified.Payload, CredentialSetRevocationPredicateTypeV1)
	if err != nil {
		return CredentialSetRevocation{}, nil, err
	}
	if err := requireExactShape(statement.Predicate, credentialSetRevocationShape()); err != nil {
		return CredentialSetRevocation{}, nil, fmt.Errorf("validate credential-set revocation predicate shape: %w", err)
	}
	var revocation CredentialSetRevocation
	if err := decodeStrictJSON(statement.Predicate, &revocation); err != nil {
		return CredentialSetRevocation{}, nil, fmt.Errorf("decode credential-set revocation predicate: %w", err)
	}
	if revocation.SchemaVersion != CredentialSetRevocationSchemaV1 || revocation.RunID != expected.RunID ||
		revocation.FixtureID != expected.GoldenRuntime.FixtureID || revocation.Issuer != binding.Issuer ||
		revocation.Audience != binding.Audience || revocation.SetHandleHash != binding.SetHandleHash ||
		revocation.MemberBindingsDigest != binding.MemberBindingsDigest || revocation.MemberCount != binding.MemberCount ||
		revocation.Status != "revoked" || revocation.IssuedAt != binding.IssuedAt || revocation.ExpiresAt != binding.ExpiresAt ||
		revocation.RevokedAt != binding.RevokedAt {
		return CredentialSetRevocation{}, nil, errors.New("credential-set revocation predicate does not exactly match the receipt and Golden fixture")
	}
	if err := validateCredentialSetMembers(revocation.Members, binding.MemberBindingsDigest, binding.MemberCount, binding.SetHandleHash); err != nil {
		return CredentialSetRevocation{}, nil, err
	}
	revokedAt, err := parseCanonicalTime(revocation.RevokedAt, "credentialSetRevocation.revokedAt")
	if err != nil {
		return CredentialSetRevocation{}, nil, err
	}
	if err := validateAuthoritySignerValidity(authority, verified, revokedAt, "credential-set revocation"); err != nil {
		return CredentialSetRevocation{}, nil, err
	}
	return revocation, verified, nil
}

func credentialSetExpectedSubject(setHandleHash string) templateauthority.ExpectedSubject {
	return templateauthority.ExpectedSubject{
		Name: "worksflow-credential-set/" + strings.TrimPrefix(setHandleHash, "sha256:"), SHA256Digest: setHandleHash,
	}
}

func validateCredentialSignerIdentities(authority credentialAuthority, verified *templateauthority.VerifiedDSSE, operation string) error {
	for _, identity := range verified.SignerIdentities {
		if _, allowed := authority.allowedIdentities[identity]; !allowed {
			return fmt.Errorf("credential-set %s signer %q is not an allowed issuer identity", operation, identity)
		}
	}
	return nil
}

func validateCredentialSetMembers(members []CredentialSetMember, expectedDigest string, expectedCount int, setHandleHash string) error {
	if len(members) != expectedCount || len(members) < 1 || len(members) > CredentialSetMaximumMembers {
		return errors.New("credential-set member count does not match the root-owned binding")
	}
	slots := make(map[string]struct{}, len(members))
	handles := make(map[string]struct{}, len(members))
	for index, member := range members {
		if !validStableID(member.Slot) || !validUUID(member.ActorID) || !validCredentialSetMemberKind(member.Kind) ||
			!validDigest(member.CredentialHandleHash) || member.CredentialHandleHash == setHandleHash {
			return fmt.Errorf("credential-set member %d is non-canonical", index)
		}
		if _, duplicate := slots[member.Slot]; duplicate {
			return errors.New("credential-set member slots must be unique")
		}
		if _, duplicate := handles[member.CredentialHandleHash]; duplicate {
			return errors.New("credential-set member credential handles must be unique")
		}
		slots[member.Slot] = struct{}{}
		handles[member.CredentialHandleHash] = struct{}{}
		if index > 0 && !credentialSetMemberLess(members[index-1], member) {
			return errors.New("credential-set members must be strictly sorted and unique by slot, actorId, kind, and credentialHandleHash")
		}
	}
	digest, err := credentialSetMemberBindingsDigest(members)
	if err != nil || digest != expectedDigest {
		return errors.New("credential-set member bindings do not match the canonical digest")
	}
	return nil
}

func validCredentialSetMemberKind(kind string) bool {
	return kind == "token" || kind == "storage-state"
}

func credentialSetMemberLess(left, right CredentialSetMember) bool {
	leftValues := [...]string{left.Slot, left.ActorID, left.Kind, left.CredentialHandleHash}
	rightValues := [...]string{right.Slot, right.ActorID, right.Kind, right.CredentialHandleHash}
	for index := range leftValues {
		if leftValues[index] == rightValues[index] {
			continue
		}
		return leftValues[index] < rightValues[index]
	}
	return false
}

func credentialSetMemberBindingsDigest(members []CredentialSetMember) (string, error) {
	projectedMembers := make([]map[string]string, 0, len(members))
	for _, member := range members {
		projectedMembers = append(projectedMembers, map[string]string{
			"actorId": member.ActorID, "credentialHandleHash": member.CredentialHandleHash,
			"kind": member.Kind, "slot": member.Slot,
		})
	}
	canonical, err := canonicalJSONBytes(map[string]any{
		"schemaVersion": CredentialSetMembersSchemaV1,
		"members":       projectedMembers,
	})
	if err != nil {
		return "", err
	}
	return sha256Digest(canonical), nil
}

func equalCredentialSetMembers(left, right []CredentialSetMember) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func publicKeyFingerprint(publicKey any) (string, error) {
	encoded, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return fmt.Sprintf("sha256:%x", digest[:]), nil
}

func parseStatement(payload []byte, predicateType string) (signedStatement, error) {
	var raw rawSignedStatement
	if err := decodeStrictJSON(payload, &raw); err != nil {
		return signedStatement{}, fmt.Errorf("decode signed in-toto statement: %w", err)
	}
	if raw.Type != templateauthority.InTotoStatementV1 || raw.PredicateType != predicateType || len(raw.Subject) != 1 || len(raw.Predicate) == 0 {
		return signedStatement{}, errors.New("signed document is not the expected single-subject in-toto statement")
	}
	return signedStatement{Type: raw.Type, Subject: raw.Subject, PredicateType: raw.PredicateType, Predicate: append([]byte(nil), raw.Predicate...)}, nil
}

func validateExpectedPromotion(expected ExpectedPromotion) error {
	if validatePromotionTarget(expected.PromotionTarget) != nil || !validUUID(expected.AuthorityNonce) ||
		!validDigest(expected.PromotionAuthorityDigest) || !validUUID(expected.RunID) || !validDigest(expected.PlanDigest) || !validDigest(expected.PrePromotionManifestDigest) ||
		validateSource(expected.Source) != nil || validateTemplateRelease(expected.TemplateRelease) != nil ||
		validateGoldenRuntimeBinding(expected.GoldenRuntime) != nil || validateCredentialSetAuthorityBinding(expected.CredentialSet) != nil ||
		!validDigest(expected.BuildContractHash) || !validStableID(expected.WriterDrainEvidenceArtifactID) ||
		!validDigest(expected.SourcePolicyAttestationDigest) || !validDigest(expected.TestInventoryDigest) || expected.VerifiedAt.IsZero() ||
		!filepath.IsAbs(expected.ArtifactRoot) || filepath.Clean(expected.ArtifactRoot) != expected.ArtifactRoot ||
		!filepath.IsAbs(expected.EvidenceSnapshotRoot) || filepath.Clean(expected.EvidenceSnapshotRoot) != expected.EvidenceSnapshotRoot ||
		!validDigest(expected.ArtifactIndexDigest) || !validDigest(expected.ReceiptBundleDigest) ||
		!validStableID(expected.ArtifactSnapshotID) || expected.ArtifactSnapshotMode != ImmutableSnapshotMode {
		return errors.New("expected promotion inputs are incomplete or non-canonical")
	}
	if _, err := parseCanonicalTime(expected.TrustedReceiptIssuedAt, "expected.trustedReceiptIssuedAt"); err != nil {
		return err
	}
	authorityIssuedAt, err := parseCanonicalTime(expected.AuthorityIssuedAt, "expected.authorityIssuedAt")
	if err != nil {
		return err
	}
	authorityExpiresAt, err := parseCanonicalTime(expected.AuthorityExpiresAt, "expected.authorityExpiresAt")
	if err != nil || !authorityExpiresAt.After(authorityIssuedAt) || authorityExpiresAt.Sub(authorityIssuedAt) > maximumPromotionAuthorityTTL ||
		!expected.VerifiedAt.UTC().Before(authorityExpiresAt) {
		return errors.New("expected promotion authority is expired or non-canonical")
	}
	if len(expected.Suites) == 0 || len(expected.TestCases) == 0 {
		return errors.New("expected promotion must include external suites and test inventory")
	}
	for index, suite := range expected.Suites {
		if !validStableID(suite.ID) || (index > 0 && expected.Suites[index-1].ID >= suite.ID) ||
			!sortedUniqueStrings(suite.RequirementIDs, requirementPattern.MatchString) ||
			!sortedUniqueStrings(suite.RequiredArtifacts, validStableID) {
			return errors.New("expected promotion suites must be canonical, sorted, and complete")
		}
	}
	playwrightCases := 0
	for index, testCase := range expected.TestCases {
		if !testCaseIDPattern.MatchString(testCase.CaseID) || (index > 0 && expected.TestCases[index-1].CaseID >= testCase.CaseID) ||
			!validStableID(testCase.SuiteID) || !sortedUniqueStrings(testCase.RequirementIDs, requirementPattern.MatchString) ||
			(len(testCase.ContractCriterionIDs) > 0 && !sortedUniqueStrings(testCase.ContractCriterionIDs, contractCriterionIDPattern.MatchString)) {
			return errors.New("expected test inventory is not canonical and sorted")
		}
		if testCase.Mode == "qualification" {
			playwrightCases++
		}
	}
	if playwrightCases == 0 {
		return errors.New("expected test inventory contains no Playwright qualification cases")
	}
	return nil
}
