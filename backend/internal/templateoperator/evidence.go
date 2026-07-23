package templateoperator

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/worksflow/builder/backend/internal/templateauthority"
	"github.com/worksflow/builder/backend/internal/templates"
)

const EvidencePreparationSchemaVersion = "template-artifact-authority-evidence-preparation/v1"

// EvidencePreparationRequest is a signer-side request. It accepts the exact
// candidate lineage and externally produced gate facts, but deliberately does
// not accept an SBOM aggregate, subject hash, signature gate, DSSE envelope, or
// transparency proof. Those values are derived from verified registry bytes,
// reviewed trust configuration, private signing keys, and the local clock.
type EvidencePreparationRequest struct {
	SchemaVersion         string                                   `json:"schemaVersion"`
	AttemptID             string                                   `json:"attemptId"`
	ReleaseID             string                                   `json:"releaseId"`
	Candidate             templates.AdmissionCandidate             `json:"candidate"`
	ArtifactReference     string                                   `json:"artifactReference"`
	ServiceSBOMs          []templates.ArtifactServiceSBOMReference `json:"serviceSboms"`
	VerificationReference string                                   `json:"verificationReference"`
	Evidence              []templates.GateEvidence                 `json:"evidence"`
	PayloadType           string                                   `json:"payloadType"`
	Signer                PrivateKeySelection                      `json:"signer"`
	Transparency          TransparencyKeySelection                 `json:"transparency"`
	RequestedBy           string                                   `json:"requestedBy"`
	EvaluatedBy           string                                   `json:"evaluatedBy"`
}

type PrivateKeySelection struct {
	KeyID          string `json:"keyId"`
	PrivateKeyFile string `json:"privateKeyFile"`
}

type TransparencyKeySelection struct {
	LogID          string `json:"logId"`
	KeyID          string `json:"keyId"`
	PrivateKeyFile string `json:"privateKeyFile"`
}

type admissionPredicate struct {
	SchemaVersion         string                   `json:"schemaVersion"`
	SubjectHash           string                   `json:"subjectHash"`
	SourceTreeHash        string                   `json:"sourceTreeHash"`
	ArtifactDigest        string                   `json:"artifactDigest"`
	SBOMDigest            string                   `json:"sbomDigest"`
	LicenseDigest         string                   `json:"licenseDigest"`
	PolicyHash            string                   `json:"policyHash"`
	VerificationReference string                   `json:"verificationReference"`
	Evidence              []templates.GateEvidence `json:"evidence"`
}

type admissionStatement struct {
	Type          string                      `json:"_type"`
	Subject       []admissionStatementSubject `json:"subject"`
	PredicateType string                      `json:"predicateType"`
	Predicate     json.RawMessage             `json:"predicate"`
}

type admissionStatementSubject struct {
	Name   string            `json:"name"`
	Digest map[string]string `json:"digest"`
}

type signedEnvelope struct {
	PayloadType string                    `json:"payloadType"`
	Payload     string                    `json:"payload"`
	Signatures  []signedEnvelopeSignature `json:"signatures"`
}

type signedEnvelopeSignature struct {
	KeyID string `json:"keyid"`
	Sig   string `json:"sig"`
}

type preparedTransparencyBundle struct {
	LogID                string                         `json:"logId"`
	TreeSize             uint64                         `json:"treeSize"`
	RootHash             string                         `json:"rootHash"`
	LeafIndex            uint64                         `json:"leafIndex"`
	IntegratedTime       int64                          `json:"integratedTime"`
	Leaf                 string                         `json:"leaf"`
	InclusionProof       []string                       `json:"inclusionProof"`
	Checkpoint           preparedTransparencyCheckpoint `json:"checkpoint"`
	SignedEntryTimestamp preparedTransparencySignature  `json:"signedEntryTimestamp"`
}

type preparedTransparencyCheckpoint struct {
	SignedAt  int64  `json:"signedAt"`
	KeyID     string `json:"keyid"`
	Signature string `json:"signature"`
}

type preparedTransparencySignature struct {
	KeyID     string `json:"keyid"`
	Signature string `json:"signature"`
}

type privateSigningKey struct {
	algorithm templateauthority.SignatureAlgorithm
	key       any
	identity  string
}

func DecodeEvidencePreparationRequest(encoded []byte) (EvidencePreparationRequest, error) {
	if len(encoded) == 0 || len(encoded) > maxAdmissionRequestBytes {
		return EvidencePreparationRequest{}, fmt.Errorf("evidence preparation request must be between 1 and %d bytes", maxAdmissionRequestBytes)
	}
	var request EvidencePreparationRequest
	if err := decodeStrictJSON(encoded, &request); err != nil {
		return EvidencePreparationRequest{}, fmt.Errorf("decode evidence preparation request: %w", err)
	}
	if request.SchemaVersion != EvidencePreparationSchemaVersion {
		return EvidencePreparationRequest{}, fmt.Errorf("schemaVersion must equal %q", EvidencePreparationSchemaVersion)
	}
	return request, nil
}

// PrepareAdmission verifies the exact OCI image and aggregate SPDX referrers,
// binds reviewed gate facts to their computed subject, creates the seventeenth
// signature gate, signs an in-toto statement, and emits a one-entry normalized
// transparency proof. It does not read Git or write PostgreSQL; Admit performs
// those independent checks before any release becomes selectable.
func PrepareAdmission(
	ctx context.Context,
	config Config,
	lookup EnvironmentLookup,
	request EvidencePreparationRequest,
	now func() time.Time,
) (AdmissionRequest, error) {
	if ctx == nil {
		return AdmissionRequest{}, errors.New("evidence preparation context is required")
	}
	if request.SchemaVersion != EvidencePreparationSchemaVersion {
		return AdmissionRequest{}, fmt.Errorf("schemaVersion must equal %q", EvidencePreparationSchemaVersion)
	}
	if now == nil {
		now = time.Now
	}
	preparedAt := now().UTC()
	if preparedAt.IsZero() {
		return AdmissionRequest{}, errors.New("evidence preparation clock returned zero")
	}
	compiled, err := compileConfig(config)
	if err != nil {
		return AdmissionRequest{}, err
	}
	if !digestPattern.MatchString(config.Authority.ExpectedPolicyHash) ||
		!digestPattern.MatchString(config.Authority.ExpectedTrustRootDigest) ||
		config.Authority.ExpectedPolicyHash != compiled.commitments.PolicyHash ||
		config.Authority.ExpectedTrustRootDigest != compiled.commitments.TrustRootDigest {
		return AdmissionRequest{}, errors.New("reviewed Artifact Authority commitments do not match the configured policy and trust material")
	}
	if request.Candidate.SBOMDigest != "" {
		return AdmissionRequest{}, errors.New("candidate.sbomDigest must be empty because it is derived from byte-verified service SBOMs")
	}
	if !containsExact(compiled.config.DSSE.AllowedPayloadTypes, request.PayloadType) {
		return AdmissionRequest{}, errors.New("payloadType is not allowed by the reviewed DSSE policy")
	}

	_, oci, sbomVerifier, err := newRegistryVerifiers(compiled, lookup)
	if err != nil {
		return AdmissionRequest{}, err
	}
	artifact, err := oci.VerifyImage(ctx, strings.TrimSpace(request.ArtifactReference))
	if err != nil {
		return AdmissionRequest{}, fmt.Errorf("verify admission artifact: %w", err)
	}
	sbomRequests, normalizedSBOMReferences, err := prepareSBOMRequests(request.Candidate, request.ServiceSBOMs)
	if err != nil {
		return AdmissionRequest{}, err
	}
	aggregate, err := sbomVerifier.VerifyAggregate(ctx, sbomRequests)
	if err != nil {
		return AdmissionRequest{}, fmt.Errorf("verify aggregate service SBOMs: %w", err)
	}

	candidate := request.Candidate
	candidate.SBOMDigest = aggregate.Digest
	authorityAttempt, err := templates.NewAuthorityAdmissionAttempt(request.AttemptID, request.RequestedBy, candidate, preparedAt)
	if err != nil {
		return AdmissionRequest{}, fmt.Errorf("normalize admission candidate: %w", err)
	}
	view := authorityAttempt.Snapshot()
	candidate = view.Candidate
	subjectHash := view.SubjectHash

	dsseKey, err := selectDSSEPrivateKey(compiled, request.Signer)
	if err != nil {
		return AdmissionRequest{}, err
	}
	logKey, err := selectTransparencyPrivateKey(compiled, request.Transparency)
	if err != nil {
		return AdmissionRequest{}, err
	}
	evidence, err := bindGateEvidence(
		request.Evidence, candidate, subjectHash, artifact.Reference.Digest,
		aggregate.Digest, compiled.commitments, request.Signer.KeyID, dsseKey.identity,
		request.AttemptID, preparedAt,
	)
	if err != nil {
		return AdmissionRequest{}, err
	}

	predicateJSON, err := json.Marshal(admissionPredicate{
		SchemaVersion: templates.ArtifactAdmissionAttestationSchemaVersion,
		SubjectHash:   subjectHash, SourceTreeHash: candidate.Source.TreeHash,
		ArtifactDigest: artifact.Reference.Digest, SBOMDigest: aggregate.Digest,
		LicenseDigest: candidate.LicenseDigest, PolicyHash: compiled.commitments.PolicyHash,
		VerificationReference: strings.TrimSpace(request.VerificationReference), Evidence: evidence,
	})
	if err != nil {
		return AdmissionRequest{}, fmt.Errorf("encode admission predicate: %w", err)
	}
	payload, err := json.Marshal(admissionStatement{
		Type: templateauthority.InTotoStatementV1,
		Subject: []admissionStatementSubject{{
			Name:   artifact.Reference.String(),
			Digest: map[string]string{"sha256": strings.TrimPrefix(artifact.Reference.Digest, "sha256:")},
		}},
		PredicateType: compiled.config.Authority.PredicateType,
		Predicate:     predicateJSON,
	})
	if err != nil {
		return AdmissionRequest{}, fmt.Errorf("encode in-toto admission statement: %w", err)
	}
	dsseSignature, err := dsseKey.sign(templateauthority.DSSEPAE(request.PayloadType, payload))
	if err != nil {
		return AdmissionRequest{}, fmt.Errorf("sign DSSE admission statement: %w", err)
	}
	envelopeJSON, err := json.Marshal(signedEnvelope{
		PayloadType: request.PayloadType,
		Payload:     base64.StdEncoding.EncodeToString(payload),
		Signatures: []signedEnvelopeSignature{{
			KeyID: request.Signer.KeyID, Sig: base64.StdEncoding.EncodeToString(dsseSignature),
		}},
	})
	if err != nil {
		return AdmissionRequest{}, fmt.Errorf("encode DSSE envelope: %w", err)
	}

	transparencyJSON, transparencyReference, err := buildTransparencyBundle(
		envelopeJSON, request.Transparency, logKey, preparedAt,
	)
	if err != nil {
		return AdmissionRequest{}, err
	}
	if err := validatePreparedEvidence(
		request, candidate, subjectHash, evidence, envelopeJSON,
		dsseKey.identity, transparencyReference, preparedAt,
	); err != nil {
		return AdmissionRequest{}, err
	}

	return AdmissionRequest{
		SchemaVersion: AdmissionSchemaVersion,
		AttemptID:     request.AttemptID, ReleaseID: request.ReleaseID, Candidate: candidate,
		Bundle: templates.ArtifactAdmissionBundle{
			ArtifactReference: artifact.Reference.String(), ServiceSBOMs: normalizedSBOMReferences,
			DSSEEnvelope: envelopeJSON, TransparencyBundle: transparencyJSON,
			VerificationReference: strings.TrimSpace(request.VerificationReference),
		},
		RequestedBy: strings.TrimSpace(request.RequestedBy),
		EvaluatedBy: strings.TrimSpace(request.EvaluatedBy),
	}, nil
}

func prepareSBOMRequests(
	candidate templates.AdmissionCandidate,
	references []templates.ArtifactServiceSBOMReference,
) ([]templateauthority.ServiceSBOMRequest, []templates.ArtifactServiceSBOMReference, error) {
	if len(references) != len(candidate.Manifest.Services) || len(references) == 0 {
		return nil, nil, errors.New("serviceSboms must cover every manifest service exactly once")
	}
	expected := make(map[string]bool, len(candidate.Manifest.Services))
	for _, service := range candidate.Manifest.Services {
		expected[strings.TrimSpace(service.ID)] = true
	}
	seen := make(map[string]bool, len(references))
	normalized := append([]templates.ArtifactServiceSBOMReference(nil), references...)
	for index := range normalized {
		item := &normalized[index]
		item.ServiceID = strings.TrimSpace(item.ServiceID)
		item.ImageReference = strings.TrimSpace(item.ImageReference)
		item.ReferrerReference = strings.TrimSpace(item.ReferrerReference)
		if !expected[item.ServiceID] || seen[item.ServiceID] || item.ImageReference == "" || item.ReferrerReference == "" {
			return nil, nil, errors.New("serviceSboms contains an unknown, duplicate, or incomplete service reference")
		}
		seen[item.ServiceID] = true
	}
	sort.Slice(normalized, func(left, right int) bool { return normalized[left].ServiceID < normalized[right].ServiceID })
	requests := make([]templateauthority.ServiceSBOMRequest, 0, len(normalized))
	for _, item := range normalized {
		requests = append(requests, templateauthority.ServiceSBOMRequest{
			ServiceID: item.ServiceID, ImageReference: item.ImageReference,
			ReferrerReference: item.ReferrerReference,
		})
	}
	return requests, normalized, nil
}

func bindGateEvidence(
	input []templates.GateEvidence,
	candidate templates.AdmissionCandidate,
	subjectHash, artifactDigest, sbomDigest string,
	commitments Commitments,
	signerKeyID, signerIdentity, invocationID string,
	preparedAt time.Time,
) ([]templates.GateEvidence, error) {
	required := make(map[templates.AdmissionGate]bool)
	for _, gate := range templates.RequiredAdmissionGates() {
		required[gate] = true
	}
	if len(input) != len(required)-1 {
		return nil, errors.New("evidence must contain exactly the 16 non-signature admission gates")
	}
	seen := make(map[templates.AdmissionGate]bool, len(input))
	evidence := make([]templates.GateEvidence, 0, len(required))
	for _, original := range input {
		item := original
		if !required[item.Gate] || item.Gate == templates.GateSignatureAttestation || seen[item.Gate] {
			return nil, fmt.Errorf("evidence contains unknown, duplicate, or caller-supplied signature gate %q", item.Gate)
		}
		seen[item.Gate] = true
		if item.Outcome != templates.EvidencePassed {
			return nil, fmt.Errorf("evidence gate %q did not pass", item.Gate)
		}
		item.SubjectHash = subjectHash
		item.Digest = strings.TrimSpace(item.Digest)
		item.Reference = strings.TrimSpace(item.Reference)
		item.Producer = strings.TrimSpace(item.Producer)
		item.InvocationID = strings.TrimSpace(item.InvocationID)
		item.ObservedAt = item.ObservedAt.UTC()
		if item.ObservedAt.IsZero() || item.ObservedAt.After(preparedAt) {
			return nil, fmt.Errorf("evidence gate %q has an invalid observation time", item.Gate)
		}
		switch item.Gate {
		case templates.GateSourceIdentity:
			item.Digest = candidate.Source.TreeHash
		case templates.GateLicenseSPDX:
			item.Digest = candidate.LicenseDigest
		case templates.GateRegistryPolicy:
			item.Digest = commitments.PolicyHash
		case templates.GateContainerBuild:
			item.Digest = artifactDigest
		case templates.GateSBOM:
			item.Digest = sbomDigest
		}
		evidence = append(evidence, item)
	}
	for gate := range required {
		if gate != templates.GateSignatureAttestation && !seen[gate] {
			return nil, fmt.Errorf("required evidence gate %q is missing", gate)
		}
	}
	evidence = append(evidence, templates.GateEvidence{
		Gate: templates.GateSignatureAttestation, Outcome: templates.EvidencePassed,
		SubjectHash: subjectHash, Digest: commitments.TrustRootDigest,
		Reference: "urn:worksflow:template-signature:" + signerKeyID,
		Producer:  signerIdentity, InvocationID: invocationID,
		ObservedAt: preparedAt.UTC().Truncate(time.Second),
	})
	sort.Slice(evidence, func(left, right int) bool { return evidence[left].Gate < evidence[right].Gate })
	return evidence, nil
}

func selectDSSEPrivateKey(compiled compiledConfig, selection PrivateKeySelection) (privateSigningKey, error) {
	configured, present := compiled.dssePolicy.Keys[selection.KeyID]
	if !present {
		return privateSigningKey{}, errors.New("signer.keyId is not present in the reviewed DSSE trust policy")
	}
	return loadMatchingPrivateKey(selection.PrivateKeyFile, configured, "DSSE signer")
}

func selectTransparencyPrivateKey(compiled compiledConfig, selection TransparencyKeySelection) (privateSigningKey, error) {
	log, present := compiled.transparencyPolicy.Logs[selection.LogID]
	if !present {
		return privateSigningKey{}, errors.New("transparency.logId is not present in the reviewed trust policy")
	}
	configured, present := log.Keys[selection.KeyID]
	if !present {
		return privateSigningKey{}, errors.New("transparency.keyId is not present in the reviewed log trust policy")
	}
	return loadMatchingPrivateKey(selection.PrivateKeyFile, configured, "transparency signer")
}

func loadMatchingPrivateKey(
	path string,
	configured templateauthority.TrustedSigner,
	label string,
) (privateSigningKey, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 {
		return privateSigningKey{}, fmt.Errorf("%s private key must be a regular non-symlink file readable only by its owner", label)
	}
	encoded, err := readRegularFile(path, maxPublicKeyBytes)
	if err != nil {
		return privateSigningKey{}, fmt.Errorf("read %s private key: %w", label, err)
	}
	block, rest := pem.Decode(encoded)
	if block == nil || block.Type != "PRIVATE KEY" || len(bytes.TrimSpace(rest)) != 0 {
		return privateSigningKey{}, fmt.Errorf("%s private key must contain exactly one PEM PKCS8 PRIVATE KEY", label)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return privateSigningKey{}, fmt.Errorf("parse %s PKCS8 private key: %w", label, err)
	}
	var public any
	switch configured.Algorithm {
	case templateauthority.AlgorithmEd25519:
		key, ok := parsed.(ed25519.PrivateKey)
		if !ok || len(key) != ed25519.PrivateKeySize {
			return privateSigningKey{}, fmt.Errorf("%s private key is not Ed25519", label)
		}
		public = key.Public()
	case templateauthority.AlgorithmECDSASHA256:
		key, ok := parsed.(*ecdsa.PrivateKey)
		if !ok || key == nil || key.Curve == nil || key.X == nil || key.Y == nil || !key.Curve.IsOnCurve(key.X, key.Y) {
			return privateSigningKey{}, fmt.Errorf("%s private key is not valid ECDSA", label)
		}
		public = &key.PublicKey
	default:
		return privateSigningKey{}, fmt.Errorf("%s uses an unsupported algorithm", label)
	}
	configuredDER, err := x509.MarshalPKIXPublicKey(configured.PublicKey)
	if err != nil {
		return privateSigningKey{}, fmt.Errorf("marshal configured %s public key: %w", label, err)
	}
	privateDER, err := x509.MarshalPKIXPublicKey(public)
	if err != nil || !bytes.Equal(privateDER, configuredDER) {
		return privateSigningKey{}, fmt.Errorf("%s private key does not match the reviewed public key", label)
	}
	return privateSigningKey{algorithm: configured.Algorithm, key: parsed, identity: configured.Identity}, nil
}

func (key privateSigningKey) sign(message []byte) ([]byte, error) {
	switch key.algorithm {
	case templateauthority.AlgorithmEd25519:
		privateKey, ok := key.key.(ed25519.PrivateKey)
		if !ok {
			return nil, errors.New("Ed25519 private key is unavailable")
		}
		return ed25519.Sign(privateKey, message), nil
	case templateauthority.AlgorithmECDSASHA256:
		privateKey, ok := key.key.(*ecdsa.PrivateKey)
		if !ok {
			return nil, errors.New("ECDSA private key is unavailable")
		}
		digest := sha256.Sum256(message)
		return ecdsa.SignASN1(rand.Reader, privateKey, digest[:])
	default:
		return nil, errors.New("unsupported private signing key algorithm")
	}
}

func buildTransparencyBundle(
	leaf []byte,
	selection TransparencyKeySelection,
	key privateSigningKey,
	preparedAt time.Time,
) ([]byte, string, error) {
	leafHash := templateauthority.RFC6962LeafHash(leaf)
	rootHash := "sha256:" + hex.EncodeToString(leafHash[:])
	timestamp := preparedAt.UTC().Truncate(time.Second).Unix()
	entry := templateauthority.TransparencyEntry{
		LogID: selection.LogID, TreeSize: 1, RootHash: rootHash,
		LeafIndex: 0, LeafHash: rootHash, IntegratedTime: timestamp,
	}
	checkpointSignature, err := key.sign(templateauthority.CheckpointSigningBytes(entry, timestamp))
	if err != nil {
		return nil, "", fmt.Errorf("sign transparency checkpoint: %w", err)
	}
	setSignature, err := key.sign(templateauthority.SignedEntryTimestampSigningBytes(entry))
	if err != nil {
		return nil, "", fmt.Errorf("sign transparency SET: %w", err)
	}
	bundle, err := json.Marshal(preparedTransparencyBundle{
		LogID: entry.LogID, TreeSize: entry.TreeSize, RootHash: entry.RootHash,
		LeafIndex: entry.LeafIndex, IntegratedTime: entry.IntegratedTime,
		Leaf: base64.StdEncoding.EncodeToString(leaf), InclusionProof: []string{},
		Checkpoint: preparedTransparencyCheckpoint{
			SignedAt: timestamp, KeyID: selection.KeyID,
			Signature: base64.StdEncoding.EncodeToString(checkpointSignature),
		},
		SignedEntryTimestamp: preparedTransparencySignature{
			KeyID: selection.KeyID, Signature: base64.StdEncoding.EncodeToString(setSignature),
		},
	})
	if err != nil {
		return nil, "", fmt.Errorf("encode transparency bundle: %w", err)
	}
	return bundle, "urn:worksflow:transparency:" + entry.LogID + ":" + rootHash, nil
}

func validatePreparedEvidence(
	request EvidencePreparationRequest,
	candidate templates.AdmissionCandidate,
	subjectHash string,
	evidence []templates.GateEvidence,
	envelopeJSON []byte,
	signerIdentity, transparencyReference string,
	preparedAt time.Time,
) error {
	attempt, err := templates.NewAdmissionAttempt(request.AttemptID, request.RequestedBy, candidate, preparedAt)
	if err != nil {
		return fmt.Errorf("validate prepared candidate: %w", err)
	}
	attempt, err = attempt.BeginValidation(preparedAt)
	if err != nil {
		return fmt.Errorf("begin prepared evidence validation: %w", err)
	}
	completed, release, err := attempt.Complete(
		request.ReleaseID,
		evidence,
		templates.SignatureEnvelope{
			Format: "dsse", SubjectHash: subjectHash,
			BundleDigest: templateauthority.SHA256Digest(envelopeJSON),
			Signer:       signerIdentity, TransparencyLogRef: transparencyReference,
			SignedAt: preparedAt.UTC().Truncate(time.Second),
		},
		request.EvaluatedBy,
		preparedAt,
	)
	if err != nil {
		return fmt.Errorf("validate prepared gate evidence: %w", err)
	}
	snapshot := completed.Snapshot()
	if release == nil || snapshot.Status != templates.AttemptApproved {
		diagnostics := make([]string, 0, len(snapshot.Findings))
		for _, finding := range snapshot.Findings {
			diagnostics = append(diagnostics, finding.Code+":"+finding.Field)
		}
		if len(diagnostics) == 0 {
			return errors.New("prepared gate evidence failed canonical admission validation")
		}
		return fmt.Errorf("prepared gate evidence failed canonical admission validation: %s", strings.Join(diagnostics, ", "))
	}
	return nil
}
