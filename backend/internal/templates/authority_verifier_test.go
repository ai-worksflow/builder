package templates

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/worksflow/builder/backend/internal/templateauthority"
)

const (
	authorityTestRegistry      = "registry.authority.example.test"
	authorityTestArtifactRepo  = "worksflow/templates/full-stack"
	authorityTestServiceRepo   = "worksflow/templates/web"
	authorityTestPayloadType   = "application/vnd.in-toto+json"
	authorityTestPredicateType = "https://worksflow.example.test/attestations/template-admission/v1"
	authorityTestLogID         = "transparency.example.test/template-admission"
	authorityTestDSSEKeyID     = "template-builder"
	authorityTestCheckpointKey = "checkpoint"
	authorityTestSETKey        = "set"
	authorityTestSigner        = "https://github.com/ai-worksflow/templates/.github/workflows/admit.yml@refs/heads/main"
	authorityTestRecordedBy    = "be8f3655-29b8-4f76-b789-adcae54c8dba"
	authorityTestReceiptID     = "53dc7b20-a795-48d8-a6cb-fe2c764d1513"
)

func TestVerifiedArtifactAuthorityVerifiesCompositeArtifactEvidence(t *testing.T) {
	fixture := newAuthorityCompositeFixture(t)

	receipt, err := fixture.authority.Verify(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("verify composite artifact evidence: %v", err)
	}
	view := receipt.Snapshot()
	if view.ID != authorityTestReceiptID || view.SchemaVersion != ArtifactAuthorityReceiptSchemaVersion ||
		view.Decision != ArtifactAuthorityDecisionPassed || view.ContentHash == "" {
		t.Fatalf("unexpected receipt identity: %#v", view)
	}
	if view.SubjectHash != fixture.request.SubjectHash || view.SourceTreeHash != fixture.candidate.Source.TreeHash ||
		view.ArtifactDigest != fixture.artifact.reference.Digest || view.SBOMDigest != fixture.sbomDigest ||
		view.ArtifactDigest == view.SourceTreeHash {
		t.Fatalf("receipt did not preserve distinct exact identities: %#v", view)
	}
	if view.PolicyHash != fixture.policyHash || view.VerifierImageDigest != fixture.verifierImageDigest ||
		view.TrustRootDigest != fixture.trustRootDigest ||
		view.Authority != (ArtifactAuthorityIdentity{ID: "worksflow-template-artifact-authority", Version: "1.0.0"}) ||
		view.VerificationReference != fixture.verificationReference {
		t.Fatalf("receipt did not use server-owned authority policy: %#v", view)
	}

	artifact := view.ArtifactDescriptor
	if artifact.Reference != fixture.artifact.reference.String() || artifact.Digest != fixture.artifact.reference.Digest ||
		artifact.MediaType != templateauthority.MediaTypeOCIImageManifest || artifact.SizeBytes != int64(len(fixture.artifact.manifest)) ||
		artifact.Config.Digest != fixture.artifact.document.Config.Digest || len(artifact.Layers) != 1 ||
		artifact.Layers[0].Digest != fixture.artifact.document.Layers[0].Digest || artifact.TotalBytes <= artifact.SizeBytes {
		t.Fatalf("receipt artifact descriptor is not byte-derived: %#v", artifact)
	}
	sbom := view.SBOMDescriptor
	if sbom.SchemaVersion != "worksflow.template-sbom-aggregate/v1" || sbom.Digest != fixture.sbomDigest ||
		sbom.ServiceCount != 1 || len(sbom.Services) != 1 {
		t.Fatalf("receipt SBOM descriptor is not aggregate-derived: %#v", sbom)
	}
	service := sbom.Services[0]
	if service.ServiceID != "web" || service.ImageReference != fixture.service.reference.String() ||
		service.ImageDigest != fixture.service.reference.Digest || service.ReferrerReference != fixture.referrer.reference.String() ||
		service.ReferrerDigest != fixture.referrer.reference.Digest || service.StatementDigest == "" ||
		service.PredicateDigest == "" || service.EvidenceHash == "" || service.SPDXVersion != "SPDX-2.3" ||
		service.DocumentNamespace != "https://sbom.example.test/web/v1" {
		t.Fatalf("receipt service SBOM descriptor is not byte-derived: %#v", service)
	}

	proof := view.Proof
	if proof.PayloadType != authorityTestPayloadType || proof.PredicateType != authorityTestPredicateType ||
		proof.PayloadDigest == "" || proof.SignatureBundleDigest != view.SignatureBundleDigest ||
		proof.TransparencyBundleDigest == "" || proof.LogID != authorityTestLogID ||
		proof.EntryUUID != view.TransparencyLog.EntryUUID || proof.LogIndex != 0 || proof.TreeSize != 1 ||
		proof.RootHash == "" || !proof.IntegratedAt.Equal(fixture.integratedAt) ||
		!proof.CheckpointSignedAt.Equal(fixture.checkpointAt) {
		t.Fatalf("receipt proof is not cryptographically derived: %#v", proof)
	}
	if !slices.Equal(proof.SignerIdentities, []string{authorityTestSigner}) ||
		view.Signature.Signer != authorityTestSigner || view.Signature.SubjectHash != view.SubjectHash ||
		view.Signature.BundleDigest != proof.SignatureBundleDigest {
		t.Fatalf("receipt signer was not derived from the trusted key: proof=%#v signature=%#v", proof, view.Signature)
	}
	if len(view.Evidence) != len(RequiredAdmissionGates()) {
		t.Fatalf("receipt evidence count = %d, want %d", len(view.Evidence), len(RequiredAdmissionGates()))
	}

	if fixture.source.calls != 1 || !reflect.DeepEqual(fixture.source.last, fixture.candidate.Source) {
		t.Fatalf("source verifier calls=%d source=%#v, want exact %#v", fixture.source.calls, fixture.source.last, fixture.candidate.Source)
	}
	canonical, err := receipt.CanonicalJSON()
	if err != nil {
		t.Fatalf("canonical receipt: %v", err)
	}
	parsed, err := ParseArtifactAuthorityReceipt(canonical)
	if err != nil {
		t.Fatalf("parse canonical receipt: %v", err)
	}
	if !reflect.DeepEqual(parsed.Snapshot(), view) {
		t.Fatalf("typed receipt changed after canonical round trip")
	}
}

func TestVerifiedArtifactAuthorityNormalizesReceiptTimeToDatabasePrecision(t *testing.T) {
	fixture := newAuthorityCompositeFixture(t)
	fixture.now = fixture.now.Add(987654321 * time.Nanosecond)

	receipt, err := fixture.authority.Verify(context.Background(), fixture.request)
	if err != nil {
		t.Fatalf("verify composite artifact evidence: %v", err)
	}
	view := receipt.Snapshot()
	want := fixture.now.UTC().Truncate(time.Microsecond)
	if !view.VerifiedAt.Equal(want) || !view.CreatedAt.Equal(want) {
		t.Fatalf("receipt times = verified %s created %s, want %s", view.VerifiedAt, view.CreatedAt, want)
	}
	if view.VerifiedAt.Nanosecond()%int(time.Microsecond) != 0 {
		t.Fatalf("verified time %s exceeds PostgreSQL microsecond precision", view.VerifiedAt)
	}
}

func TestVerifiedArtifactAuthorityRejectsCompositeTampering(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, *authorityCompositeFixture)
	}{
		{
			name: "source verification error",
			mutate: func(_ *testing.T, fixture *authorityCompositeFixture) {
				fixture.source.err = errors.New("source tree bytes do not match the pinned commit")
			},
		},
		{
			name: "OCI manifest bytes",
			mutate: func(_ *testing.T, fixture *authorityCompositeFixture) {
				fixture.registry.mutateManifest(fixture.artifact.reference.String(), func(data []byte) []byte {
					return append(data, ' ')
				})
			},
		},
		{
			name: "SBOM aggregate mismatch",
			mutate: func(t *testing.T, fixture *authorityCompositeFixture) {
				fixture.candidate.SBOMDigest = authorityTestDigest([]byte("different candidate SBOM aggregate"))
				fixture.rebuildSignedBundle(t, nil)
			},
		},
		{
			name: "signed predicate candidate subject",
			mutate: func(t *testing.T, fixture *authorityCompositeFixture) {
				fixture.rebuildSignedBundle(t, func(predicate *artifactAdmissionPredicate) {
					predicate.SubjectHash = authorityTestDigest([]byte("different candidate subject"))
				})
			},
		},
		{
			name: "signed predicate policy",
			mutate: func(t *testing.T, fixture *authorityCompositeFixture) {
				fixture.rebuildSignedBundle(t, func(predicate *artifactAdmissionPredicate) {
					predicate.PolicyHash = authorityTestDigest([]byte("different authority policy"))
				})
			},
		},
		{
			name: "signed gate digest",
			mutate: func(t *testing.T, fixture *authorityCompositeFixture) {
				fixture.rebuildSignedBundle(t, func(predicate *artifactAdmissionPredicate) {
					for index := range predicate.Evidence {
						if predicate.Evidence[index].Gate == GateSourceIdentity {
							predicate.Evidence[index].Digest = authorityTestDigest([]byte("different source tree"))
							return
						}
					}
					t.Fatal("source identity gate missing from signed predicate")
				})
			},
		},
		{
			name: "DSSE signature",
			mutate: func(t *testing.T, fixture *authorityCompositeFixture) {
				var envelope authorityTestDSSEEnvelope
				authorityTestUnmarshal(t, fixture.request.Bundle.DSSEEnvelope, &envelope)
				signature, err := base64.StdEncoding.DecodeString(envelope.Signatures[0].Signature)
				if err != nil {
					t.Fatal(err)
				}
				signature[0] ^= 1
				envelope.Signatures[0].Signature = base64.StdEncoding.EncodeToString(signature)
				fixture.request.Bundle.DSSEEnvelope = authorityTestJSON(t, envelope)
			},
		},
		{
			name: "transparency leaf",
			mutate: func(t *testing.T, fixture *authorityCompositeFixture) {
				var bundle authorityTestTransparencyBundle
				authorityTestUnmarshal(t, fixture.request.Bundle.TransparencyBundle, &bundle)
				bundle.Leaf = base64.StdEncoding.EncodeToString([]byte("different canonical DSSE envelope"))
				fixture.request.Bundle.TransparencyBundle = authorityTestJSON(t, bundle)
			},
		},
		{
			name: "transparency inclusion proof",
			mutate: func(t *testing.T, fixture *authorityCompositeFixture) {
				var bundle authorityTestTransparencyBundle
				authorityTestUnmarshal(t, fixture.request.Bundle.TransparencyBundle, &bundle)
				// A tree of size one has an empty proof. Any supplied node is an
				// overlong RFC6962 proof and must be rejected.
				bundle.InclusionProof = append(bundle.InclusionProof, authorityTestDigest([]byte("forged sibling")))
				fixture.request.Bundle.TransparencyBundle = authorityTestJSON(t, bundle)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newAuthorityCompositeFixture(t)
			test.mutate(t, fixture)

			receipt, err := fixture.authority.Verify(context.Background(), fixture.request)
			if err == nil {
				t.Fatalf("tampered composite evidence produced receipt %#v", receipt.Snapshot())
			}
			if receipt.Snapshot().ID != "" {
				t.Fatalf("tampered composite evidence returned a non-zero receipt with error %v: %#v", err, receipt.Snapshot())
			}
			if fixture.source.calls != 1 || !reflect.DeepEqual(fixture.source.last, fixture.candidate.Source) {
				t.Fatalf("source verifier calls=%d source=%#v, want one exact call for %#v", fixture.source.calls, fixture.source.last, fixture.candidate.Source)
			}
		})
	}
}

type authorityCompositeFixture struct {
	now                   time.Time
	integratedAt          time.Time
	checkpointAt          time.Time
	policyHash            string
	trustRootDigest       string
	verifierImageDigest   string
	verificationReference string
	registry              *authorityTestRegistryClient
	source                *authorityTestSourceVerifier
	artifact              authorityTestOCIArtifact
	service               authorityTestOCIArtifact
	referrer              authorityTestOCIArtifact
	sbomDigest            string
	candidate             AdmissionCandidate
	dssePrivate           ed25519.PrivateKey
	dsse                  *templateauthority.DSSEVerifier
	checkpointPrivate     ed25519.PrivateKey
	setPrivate            ed25519.PrivateKey
	authority             *VerifiedArtifactAuthority
	request               ArtifactAuthorityVerifyRequest
}

func newAuthorityCompositeFixture(t *testing.T) *authorityCompositeFixture {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	fixture := &authorityCompositeFixture{
		now: now, integratedAt: now.Add(-2 * time.Minute), checkpointAt: now.Add(-time.Minute),
		policyHash:            authorityTestDigest([]byte("template artifact policy v1")),
		trustRootDigest:       authorityTestDigest([]byte("template artifact trust root v1")),
		verifierImageDigest:   authorityTestDigest([]byte("template artifact verifier image v1")),
		verificationReference: "urn:worksflow:template-verification:composite-fixture-v1",
		registry:              newAuthorityTestRegistryClient(),
	}

	fixture.artifact = fixture.registry.addImage(t, authorityTestArtifactRepo,
		[]byte(`{"architecture":"amd64","os":"linux","kind":"full-stack-template"}`),
		[][]byte{[]byte("immutable full-stack template artifact\n")})
	fixture.service = fixture.registry.addImage(t, authorityTestServiceRepo,
		[]byte(`{"architecture":"amd64","os":"linux","service":"web"}`),
		[][]byte{[]byte("immutable web service layer\n")})
	fixture.referrer = fixture.registry.addSPDXReferrer(t, authorityTestServiceRepo, fixture.service, "web")

	oci, err := templateauthority.NewOCIVerifier(fixture.registry, templateauthority.RegistryPolicy{
		Repositories: []templateauthority.RepositoryRule{
			{Host: authorityTestRegistry, Repository: authorityTestArtifactRepo},
			{Host: authorityTestRegistry, Repository: authorityTestServiceRepo},
		},
	}, templateauthority.Limits{})
	if err != nil {
		t.Fatalf("configure OCI verifier: %v", err)
	}
	sbom, err := templateauthority.NewSBOMVerifier(oci)
	if err != nil {
		t.Fatalf("configure SBOM verifier: %v", err)
	}
	sbomRequest := templateauthority.ServiceSBOMRequest{
		ServiceID: "web", ImageReference: fixture.service.reference.String(),
		ReferrerReference: fixture.referrer.reference.String(),
	}
	aggregate, err := sbom.VerifyAggregate(context.Background(), []templateauthority.ServiceSBOMRequest{sbomRequest})
	if err != nil {
		t.Fatalf("derive fixture SBOM aggregate from registry bytes: %v", err)
	}
	fixture.sbomDigest = aggregate.Digest
	fixture.candidate = validCandidate("authority-web", "web")
	fixture.candidate.SBOMDigest = fixture.sbomDigest
	fixture.source = &authorityTestSourceVerifier{want: fixture.candidate.Source}

	dssePublic, dssePrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate DSSE key: %v", err)
	}
	fixture.dssePrivate = dssePrivate
	fixture.dsse, err = templateauthority.NewDSSEVerifier(templateauthority.DSSETrustPolicy{
		Keys: map[string]templateauthority.TrustedSigner{
			authorityTestDSSEKeyID: {
				Algorithm: templateauthority.AlgorithmEd25519,
				PublicKey: dssePublic,
				Identity:  authorityTestSigner,
			},
		},
		AllowedPayloadTypes:   []string{authorityTestPayloadType},
		AllowedPredicateTypes: []string{authorityTestPredicateType},
		MinSignatures:         1,
	})
	if err != nil {
		t.Fatalf("configure DSSE verifier: %v", err)
	}

	checkpointPublic, checkpointPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate transparency checkpoint key: %v", err)
	}
	setPublic, setPrivate, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate transparency SET key: %v", err)
	}
	fixture.checkpointPrivate = checkpointPrivate
	fixture.setPrivate = setPrivate
	transparency, err := templateauthority.NewTransparencyVerifier(templateauthority.TransparencyTrustPolicy{
		Logs: map[string]templateauthority.TrustedTransparencyLog{
			authorityTestLogID: {Keys: map[string]templateauthority.TrustedSigner{
				authorityTestCheckpointKey: {
					Algorithm: templateauthority.AlgorithmEd25519,
					PublicKey: checkpointPublic,
					Identity:  "checkpoint@transparency.example.test",
				},
				authorityTestSETKey: {
					Algorithm: templateauthority.AlgorithmEd25519,
					PublicKey: setPublic,
					Identity:  "set@transparency.example.test",
				},
			}},
		},
		MaxEntryAge: 24 * time.Hour, MaxFutureSkew: 2 * time.Minute,
	})
	if err != nil {
		t.Fatalf("configure transparency verifier: %v", err)
	}

	fixture.authority, err = NewVerifiedArtifactAuthority(VerifiedArtifactAuthorityConfig{
		AuthorityID: "worksflow-template-artifact-authority", AuthorityVersion: "1.0.0",
		VerifierImageDigest: fixture.verifierImageDigest, PolicyHash: fixture.policyHash,
		TrustRootDigest: fixture.trustRootDigest, PredicateType: authorityTestPredicateType,
		Source: fixture.source, OCI: oci, SBOM: sbom, DSSE: fixture.dsse, Transparency: transparency,
		DependencyReadiness: func(context.Context) error { return nil },
		Clock:               func() time.Time { return fixture.now },
		NewReceiptID:        func() string { return authorityTestReceiptID },
	})
	if err != nil {
		t.Fatalf("configure verified artifact authority: %v", err)
	}
	fixture.rebuildSignedBundle(t, nil)
	return fixture
}

func (fixture *authorityCompositeFixture) rebuildSignedBundle(
	t *testing.T,
	mutatePredicate func(*artifactAdmissionPredicate),
) {
	t.Helper()
	subject, err := subjectHash(fixture.candidate)
	if err != nil {
		t.Fatalf("hash fixture candidate: %v", err)
	}
	evidence := authorityTestEvidence(fixture, subject)
	predicate := artifactAdmissionPredicate{
		SchemaVersion: ArtifactAdmissionAttestationSchemaVersion,
		SubjectHash:   subject, SourceTreeHash: fixture.candidate.Source.TreeHash,
		ArtifactDigest: fixture.artifact.reference.Digest, SBOMDigest: fixture.sbomDigest,
		LicenseDigest: fixture.candidate.LicenseDigest, PolicyHash: fixture.policyHash,
		VerificationReference: fixture.verificationReference, Evidence: evidence,
	}
	if mutatePredicate != nil {
		mutatePredicate(&predicate)
	}
	predicateJSON := authorityTestJSON(t, predicate)
	statement := artifactAdmissionStatement{
		Type: templateauthority.InTotoStatementV1,
		Subject: []artifactAdmissionStatementSubject{{
			Name: fixture.artifact.reference.String(),
			Digest: map[string]string{
				"sha256": trimAuthorityTestDigest(fixture.artifact.reference.Digest),
			},
		}},
		PredicateType: authorityTestPredicateType,
		Predicate:     predicateJSON,
	}
	payload := authorityTestJSON(t, statement)
	signature := ed25519.Sign(fixture.dssePrivate, templateauthority.DSSEPAE(authorityTestPayloadType, payload))
	envelope := authorityTestDSSEEnvelope{
		PayloadType: authorityTestPayloadType,
		Payload:     base64.StdEncoding.EncodeToString(payload),
		Signatures: []authorityTestDSSESignature{{
			KeyID: authorityTestDSSEKeyID, Signature: base64.StdEncoding.EncodeToString(signature),
		}},
	}
	envelopeJSON := authorityTestJSON(t, envelope)
	verifiedDSSE, err := fixture.dsse.Verify(envelopeJSON, templateauthority.ExpectedSubject{
		Name: fixture.artifact.reference.String(), SHA256Digest: fixture.artifact.reference.Digest,
	})
	if err != nil {
		t.Fatalf("verify fixture DSSE before transparency binding: %v", err)
	}

	transparencyJSON := authorityTestTransparencyJSON(t, fixture, verifiedDSSE.CanonicalEnvelope)
	fixture.request = ArtifactAuthorityVerifyRequest{
		Candidate: fixture.candidate, SubjectHash: subject,
		Bundle: ArtifactAdmissionBundle{
			ArtifactReference: fixture.artifact.reference.String(),
			ServiceSBOMs: []ArtifactServiceSBOMReference{{
				ServiceID: "web", ImageReference: fixture.service.reference.String(),
				ReferrerReference: fixture.referrer.reference.String(),
			}},
			DSSEEnvelope: envelopeJSON, TransparencyBundle: transparencyJSON,
			VerificationReference: fixture.verificationReference,
		},
		RecordedBy: authorityTestRecordedBy,
	}
}

func authorityTestEvidence(fixture *authorityCompositeFixture, subject string) []GateEvidence {
	gates := RequiredAdmissionGates()
	sort.Slice(gates, func(left, right int) bool { return gates[left] < gates[right] })
	evidence := make([]GateEvidence, 0, len(gates))
	for _, gate := range gates {
		digest := authorityTestDigest([]byte("gate:" + gate))
		switch gate {
		case GateSourceIdentity:
			digest = fixture.candidate.Source.TreeHash
		case GateLicenseSPDX:
			digest = fixture.candidate.LicenseDigest
		case GateRegistryPolicy:
			digest = fixture.policyHash
		case GateContainerBuild:
			digest = fixture.artifact.reference.Digest
		case GateSBOM:
			digest = fixture.sbomDigest
		}
		evidence = append(evidence, GateEvidence{
			Gate: gate, Outcome: EvidencePassed, SubjectHash: subject, Digest: digest,
			Reference: "urn:worksflow:template-evidence:" + string(gate),
			Producer:  "worksflow-template-authority-test", InvocationID: "fixture-" + string(gate),
			ObservedAt: fixture.now.Add(-3 * time.Minute),
		})
	}
	return evidence
}

type authorityTestSourceVerifier struct {
	want  TemplateSource
	err   error
	calls int
	last  TemplateSource
}

func (verifier *authorityTestSourceVerifier) VerifySource(_ context.Context, source TemplateSource) error {
	verifier.calls++
	verifier.last = source
	if !reflect.DeepEqual(source, verifier.want) {
		return errors.New("source verifier received a different source")
	}
	return verifier.err
}

func (*authorityTestSourceVerifier) Readiness(context.Context) error { return nil }

type authorityTestRegistryClient struct {
	mu        sync.Mutex
	manifests map[string][]byte
	blobs     map[string][]byte
}

func newAuthorityTestRegistryClient() *authorityTestRegistryClient {
	return &authorityTestRegistryClient{manifests: map[string][]byte{}, blobs: map[string][]byte{}}
}

func (client *authorityTestRegistryClient) FetchManifest(
	_ context.Context,
	reference templateauthority.ExactReference,
) (templateauthority.RegistryRead, error) {
	client.mu.Lock()
	data, found := client.manifests[reference.String()]
	data = bytes.Clone(data)
	client.mu.Unlock()
	if !found {
		return templateauthority.RegistryRead{}, errors.New("fixture manifest not found")
	}
	return templateauthority.RegistryRead{
		Body: io.NopCloser(bytes.NewReader(data)), ServingHost: reference.Host,
	}, nil
}

func (client *authorityTestRegistryClient) FetchBlob(
	_ context.Context,
	repository templateauthority.ExactReference,
	descriptor templateauthority.Descriptor,
) (templateauthority.RegistryRead, error) {
	client.mu.Lock()
	data, found := client.blobs[descriptor.Digest]
	data = bytes.Clone(data)
	client.mu.Unlock()
	if !found {
		return templateauthority.RegistryRead{}, errors.New("fixture blob not found")
	}
	return templateauthority.RegistryRead{
		Body: io.NopCloser(bytes.NewReader(data)), ServingHost: repository.Host,
	}, nil
}

func (client *authorityTestRegistryClient) mutateManifest(reference string, mutate func([]byte) []byte) {
	client.mu.Lock()
	defer client.mu.Unlock()
	client.manifests[reference] = mutate(bytes.Clone(client.manifests[reference]))
}

type authorityTestOCIManifest struct {
	SchemaVersion int                            `json:"schemaVersion"`
	MediaType     string                         `json:"mediaType"`
	ArtifactType  string                         `json:"artifactType,omitempty"`
	Config        templateauthority.Descriptor   `json:"config"`
	Layers        []templateauthority.Descriptor `json:"layers"`
	Subject       *templateauthority.Descriptor  `json:"subject,omitempty"`
}

type authorityTestOCIArtifact struct {
	reference templateauthority.ExactReference
	document  authorityTestOCIManifest
	manifest  []byte
}

func (client *authorityTestRegistryClient) addImage(
	t *testing.T,
	repository string,
	config []byte,
	layers [][]byte,
) authorityTestOCIArtifact {
	t.Helper()
	document := authorityTestOCIManifest{
		SchemaVersion: 2, MediaType: templateauthority.MediaTypeOCIImageManifest,
		Config: authorityTestDescriptor(templateauthority.MediaTypeOCIImageConfig, config),
		Layers: make([]templateauthority.Descriptor, 0, len(layers)),
	}
	for _, layer := range layers {
		document.Layers = append(document.Layers, authorityTestDescriptor(templateauthority.MediaTypeOCILayer, layer))
	}
	return client.addArtifact(t, repository, document, config, layers)
}

func (client *authorityTestRegistryClient) addSPDXReferrer(
	t *testing.T,
	repository string,
	image authorityTestOCIArtifact,
	serviceID string,
) authorityTestOCIArtifact {
	t.Helper()
	predicate := map[string]any{
		"spdxVersion": "SPDX-2.3", "dataLicense": "CC0-1.0", "SPDXID": "SPDXRef-DOCUMENT",
		"name": serviceID + " SBOM", "documentNamespace": "https://sbom.example.test/" + serviceID + "/v1",
		"creationInfo": map[string]any{
			"created": "2026-07-18T00:00:00Z", "creators": []string{"Tool: worksflow-template-authority"},
		},
		"packages": []any{map[string]any{
			"SPDXID": "SPDXRef-Package-Root", "name": serviceID,
			"downloadLocation": "https://packages.example.test/" + serviceID,
			"licenseConcluded": "Apache-2.0",
		}},
	}
	statement := map[string]any{
		"_type": templateauthority.InTotoStatementV1,
		"subject": []any{map[string]any{
			"name":   image.reference.String(),
			"digest": map[string]any{"sha256": trimAuthorityTestDigest(image.reference.Digest)},
		}},
		"predicateType": templateauthority.PredicateTypeSPDX,
		"predicate":     predicate,
	}
	statementJSON := authorityTestJSON(t, statement)
	config := []byte(`{}`)
	document := authorityTestOCIManifest{
		SchemaVersion: 2, MediaType: templateauthority.MediaTypeOCIImageManifest,
		ArtifactType: templateauthority.MediaTypeInTotoStatement,
		Config:       authorityTestDescriptor(templateauthority.MediaTypeOCIEmptyConfig, config),
		Layers: []templateauthority.Descriptor{
			authorityTestDescriptor(templateauthority.MediaTypeInTotoStatement, statementJSON),
		},
		Subject: &templateauthority.Descriptor{
			MediaType: templateauthority.MediaTypeOCIImageManifest,
			Digest:    image.reference.Digest, Size: int64(len(image.manifest)),
		},
	}
	return client.addArtifact(t, repository, document, config, [][]byte{statementJSON})
}

func (client *authorityTestRegistryClient) addArtifact(
	t *testing.T,
	repository string,
	document authorityTestOCIManifest,
	config []byte,
	layers [][]byte,
) authorityTestOCIArtifact {
	t.Helper()
	manifest := authorityTestJSON(t, document)
	reference := templateauthority.ExactReference{
		Host: authorityTestRegistry, Repository: repository, Digest: authorityTestDigest(manifest),
	}
	client.mu.Lock()
	client.manifests[reference.String()] = bytes.Clone(manifest)
	client.blobs[document.Config.Digest] = bytes.Clone(config)
	for index, descriptor := range document.Layers {
		client.blobs[descriptor.Digest] = bytes.Clone(layers[index])
	}
	client.mu.Unlock()
	return authorityTestOCIArtifact{reference: reference, document: document, manifest: manifest}
}

func authorityTestDescriptor(mediaType string, data []byte) templateauthority.Descriptor {
	return templateauthority.Descriptor{MediaType: mediaType, Digest: authorityTestDigest(data), Size: int64(len(data))}
}

type authorityTestDSSEEnvelope struct {
	PayloadType string                       `json:"payloadType"`
	Payload     string                       `json:"payload"`
	Signatures  []authorityTestDSSESignature `json:"signatures"`
}

type authorityTestDSSESignature struct {
	KeyID     string `json:"keyid"`
	Signature string `json:"sig"`
}

type authorityTestTransparencyBundle struct {
	LogID                string                              `json:"logId"`
	TreeSize             uint64                              `json:"treeSize"`
	RootHash             string                              `json:"rootHash"`
	LeafIndex            uint64                              `json:"leafIndex"`
	IntegratedTime       int64                               `json:"integratedTime"`
	Leaf                 string                              `json:"leaf"`
	InclusionProof       []string                            `json:"inclusionProof"`
	Checkpoint           authorityTestTransparencyCheckpoint `json:"checkpoint"`
	SignedEntryTimestamp authorityTestTransparencySignature  `json:"signedEntryTimestamp"`
}

type authorityTestTransparencyCheckpoint struct {
	SignedAt  int64  `json:"signedAt"`
	KeyID     string `json:"keyid"`
	Signature string `json:"signature"`
}

type authorityTestTransparencySignature struct {
	KeyID     string `json:"keyid"`
	Signature string `json:"signature"`
}

func authorityTestTransparencyJSON(
	t *testing.T,
	fixture *authorityCompositeFixture,
	leaf []byte,
) []byte {
	t.Helper()
	leafHash := templateauthority.RFC6962LeafHash(leaf)
	rootHash := authorityTestHashValue(leafHash)
	entry := templateauthority.TransparencyEntry{
		LogID: authorityTestLogID, TreeSize: 1, RootHash: rootHash,
		LeafIndex: 0, LeafHash: rootHash, IntegratedTime: fixture.integratedAt.Unix(),
	}
	bundle := authorityTestTransparencyBundle{
		LogID: authorityTestLogID, TreeSize: 1, RootHash: rootHash, LeafIndex: 0,
		IntegratedTime: fixture.integratedAt.Unix(), Leaf: base64.StdEncoding.EncodeToString(leaf),
		InclusionProof: []string{},
		Checkpoint: authorityTestTransparencyCheckpoint{
			SignedAt: fixture.checkpointAt.Unix(), KeyID: authorityTestCheckpointKey,
		},
		SignedEntryTimestamp: authorityTestTransparencySignature{KeyID: authorityTestSETKey},
	}
	bundle.Checkpoint.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(
		fixture.checkpointPrivate,
		templateauthority.CheckpointSigningBytes(entry, bundle.Checkpoint.SignedAt),
	))
	bundle.SignedEntryTimestamp.Signature = base64.StdEncoding.EncodeToString(ed25519.Sign(
		fixture.setPrivate,
		templateauthority.SignedEntryTimestampSigningBytes(entry),
	))
	return authorityTestJSON(t, bundle)
}

func authorityTestDigest(data []byte) string {
	hash := sha256.Sum256(data)
	return authorityTestHashValue(hash)
}

func authorityTestHashValue(hash [sha256.Size]byte) string {
	return "sha256:" + hex.EncodeToString(hash[:])
}

func trimAuthorityTestDigest(value string) string {
	return value[len("sha256:"):]
}

func authorityTestJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func authorityTestUnmarshal(t *testing.T, encoded []byte, target any) {
	t.Helper()
	if err := json.Unmarshal(encoded, target); err != nil {
		t.Fatal(err)
	}
}
