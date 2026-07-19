package qualificationreceipt

import (
	"errors"
	"fmt"
	"strings"

	"github.com/worksflow/builder/backend/internal/templateauthority"
)

func (verifier *Verifier) verifyEncryptionAttestation(
	root string,
	index ArtifactIndex,
	receipt QualificationReceipt,
	expected ExpectedPromotion,
	artifacts verifiedArtifactSet,
) ([]string, error) {
	descriptor, exists := artifacts.byID[EncryptionAttestationArtifactID]
	if !exists || descriptor.Type != ArtifactTypeEncryptionAttestation || descriptor.Classification != ClassificationDistributable {
		return nil, errors.New("trusted KMS encryption attestation artifact is missing or has the wrong type")
	}
	manifestDigest, expectedArtifacts, err := encryptionManifest(index)
	if err != nil {
		return nil, err
	}
	envelope, err := readVerifiedArtifact(root, descriptor, maxReceiptBytes)
	if err != nil {
		return nil, err
	}
	verified, err := verifier.encryptionAuthority.verifier.Verify(envelope, templateauthority.ExpectedSubject{
		Name:         encryptionSubjectName(expected.RunID),
		SHA256Digest: manifestDigest,
	})
	if err != nil {
		return nil, fmt.Errorf("verify trusted KMS encryption attestation DSSE: %w", err)
	}
	for _, identity := range verified.SignerIdentities {
		if _, allowed := verifier.encryptionAuthority.allowedIdentities[identity]; !allowed {
			return nil, fmt.Errorf("encryption attestation signer %q is not an allowed KMS identity", identity)
		}
	}
	statement, err := parseStatement(verified.Payload, EncryptionPredicateTypeV1)
	if err != nil {
		return nil, err
	}
	if err := requireExactShape(statement.Predicate, encryptionAttestationShape()); err != nil {
		return nil, fmt.Errorf("validate encryption attestation predicate shape: %w", err)
	}
	var attestation EncryptionAttestation
	if err := decodeStrictJSON(statement.Predicate, &attestation); err != nil {
		return nil, fmt.Errorf("decode encryption attestation predicate: %w", err)
	}
	if attestation.SchemaVersion != EncryptionAttestationSchemaV1 || attestation.RunID != expected.RunID ||
		attestation.PlanDigest != expected.PlanDigest || attestation.TemplateRelease != expected.TemplateRelease ||
		attestation.ManifestDigest != manifestDigest || len(attestation.Artifacts) != len(expectedArtifacts) {
		return nil, errors.New("encryption attestation does not bind the exact run, plan, TemplateRelease, and ciphertext manifest")
	}
	completedAt, _ := parseCanonicalTime(receipt.CompletedAt, "receipt.completedAt")
	revokedAt, err := parseCanonicalTime(receipt.CredentialSet.RevokedAt, "credentialSet.revokedAt")
	if err != nil {
		return nil, err
	}
	for index, artifact := range attestation.Artifacts {
		expectedArtifact := expectedArtifacts[index]
		descriptorMatches := artifact.ArtifactID == expectedArtifact.ArtifactID && artifact.Path == expectedArtifact.Path &&
			artifact.CiphertextDigest == expectedArtifact.CiphertextDigest && artifact.SizeBytes == expectedArtifact.SizeBytes &&
			artifact.KeyResource == expectedArtifact.KeyResource && artifact.KeyVersion == expectedArtifact.KeyVersion &&
			artifact.WrappedKeyDigest == expectedArtifact.WrappedKeyDigest && artifact.AdditionalDataHash == expectedArtifact.AdditionalDataHash &&
			artifact.EncryptionDescriptorDigest == expectedArtifact.EncryptionDescriptorDigest && artifact.Status == "encrypted-by-kms"
		if !descriptorMatches {
			return nil, fmt.Errorf("encryption attestation artifact %d does not match the sealed encrypted descriptor", index)
		}
		encryptedAt, err := parseCanonicalTime(artifact.EncryptedAt, "encryptionAttestation.artifact.encryptedAt")
		if err != nil {
			return nil, err
		}
		disposedAt, err := parseCanonicalTime(artifact.PlaintextDispositionAt, "encryptionAttestation.artifact.plaintextDispositionAt")
		if err != nil {
			return nil, err
		}
		if !encryptedAt.After(completedAt) || disposedAt.Before(encryptedAt) ||
			(artifact.PlaintextDisposition == "never-persisted" && !disposedAt.Equal(encryptedAt)) ||
			(artifact.PlaintextDisposition == "deleted" && !disposedAt.After(encryptedAt)) ||
			(artifact.PlaintextDisposition != "never-persisted" && artifact.PlaintextDisposition != "deleted") {
			return nil, errors.New("KMS attestation does not prove strict encryption and plaintext disposition chronology")
		}
	}
	issuedAt, err := parseCanonicalTime(attestation.IssuedAt, "encryptionAttestation.issuedAt")
	if err != nil {
		return nil, err
	}
	if err := validateAuthoritySignerValidity(verifier.encryptionAuthority, verified, issuedAt, "KMS encryption attestation"); err != nil {
		return nil, err
	}
	receiptIssuedAt, _ := parseCanonicalTime(receipt.IssuedAt, "receipt.issuedAt")
	for _, artifact := range attestation.Artifacts {
		disposedAt, _ := parseCanonicalTime(artifact.PlaintextDispositionAt, "encryptionAttestation.artifact.plaintextDispositionAt")
		if !issuedAt.After(disposedAt) {
			return nil, errors.New("KMS attestation must be issued after every plaintext disposition")
		}
	}
	if !revokedAt.After(issuedAt) || !receiptIssuedAt.After(revokedAt) {
		return nil, errors.New("strict chronology must be encryption/plaintext disposition, KMS attestation, credential revocation, then receipt issuance")
	}
	return append([]string(nil), verified.SignerIdentities...), nil
}

func encryptionManifest(index ArtifactIndex) (string, []EncryptionAttestedArtifact, error) {
	artifacts := make([]EncryptionAttestedArtifact, 0)
	for _, descriptor := range index.Artifacts {
		if descriptor.Classification != ClassificationRestrictedEncrypted {
			continue
		}
		if descriptor.Encryption == nil {
			return "", nil, fmt.Errorf("encrypted artifact %q has no encryption descriptor", descriptor.ID)
		}
		wrappedKey, err := decodeCanonicalBase64(descriptor.Encryption.WrappedKey, 16, 16<<10)
		if err != nil {
			return "", nil, fmt.Errorf("encrypted artifact %q wrapped key is invalid", descriptor.ID)
		}
		encryptionDescriptor, err := canonicalJSONBytes(descriptor.Encryption)
		if err != nil {
			return "", nil, fmt.Errorf("encode encrypted artifact %q descriptor: %w", descriptor.ID, err)
		}
		artifacts = append(artifacts, EncryptionAttestedArtifact{
			ArtifactID: descriptor.ID, Path: descriptor.Path, CiphertextDigest: descriptor.SHA256,
			SizeBytes: descriptor.SizeBytes, KeyResource: descriptor.Encryption.KeyResource,
			KeyVersion: descriptor.Encryption.KeyVersion, WrappedKeyDigest: sha256Digest(wrappedKey),
			AdditionalDataHash:         descriptor.Encryption.AdditionalDataHash,
			EncryptionDescriptorDigest: sha256Digest(encryptionDescriptor), Status: "encrypted-by-kms",
		})
	}
	if len(artifacts) == 0 {
		return "", nil, errors.New("encryption manifest contains no encrypted artifacts")
	}
	projection := map[string]any{
		"schemaVersion":   "worksflow-evidence-encryption-manifest/v1",
		"runId":           index.RunID,
		"planDigest":      index.PlanDigest,
		"templateRelease": index.TemplateRelease,
		"artifacts":       artifacts,
	}
	canonical, err := canonicalJSONBytes(projection)
	if err != nil {
		return "", nil, fmt.Errorf("encode encryption manifest: %w", err)
	}
	return sha256Digest(canonical), artifacts, nil
}

func encryptionSubjectName(runID string) string {
	return "worksflow-qualification-encryption/" + strings.TrimSpace(runID)
}
