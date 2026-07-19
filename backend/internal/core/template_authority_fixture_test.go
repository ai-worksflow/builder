package core

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/templates"
	"gorm.io/gorm"
)

type coreTemplateArtifactAuthorityFake struct{}

func (coreTemplateArtifactAuthorityFake) Readiness(context.Context) error { return nil }

func (coreTemplateArtifactAuthorityFake) Verify(
	_ context.Context,
	request templates.ArtifactAuthorityVerifyRequest,
) (templates.ArtifactAuthorityReceipt, error) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	integratedAt, checkpointAt := now.Add(-2*time.Second), now.Add(-time.Second)
	artifactDigest := hashText("core-template-artifact:" + request.SubjectHash)
	signatureDigest := hashText("core-template-signature:" + request.SubjectHash)
	entryUUID := "entry:" + strings.ReplaceAll(uuid.NewString(), "-", "")
	config := templates.ArtifactBlobDescriptor{
		MediaType: "application/vnd.oci.image.config.v1+json",
		Digest:    hashText("core-template-config:" + request.SubjectHash), SizeBytes: 64,
	}
	layer := templates.ArtifactBlobDescriptor{
		MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
		Digest:    hashText("core-template-layer:" + request.SubjectHash), SizeBytes: 128,
	}
	services := make([]templates.ArtifactSBOMServiceDescriptor, 0, len(request.Candidate.Manifest.Services))
	for _, service := range request.Candidate.Manifest.Services {
		imageDigest := hashText("core-template-image:" + service.ID + request.SubjectHash)
		referrerDigest := hashText("core-template-referrer:" + service.ID + request.SubjectHash)
		services = append(services, templates.ArtifactSBOMServiceDescriptor{
			ServiceID:         service.ID,
			ImageReference:    "registry.example/core/" + service.ID + "@" + imageDigest,
			ImageDigest:       imageDigest,
			ReferrerReference: "registry.example/core/" + service.ID + "-sbom@" + referrerDigest,
			ReferrerDigest:    referrerDigest,
			StatementDigest:   hashText("core-template-statement:" + service.ID + request.SubjectHash),
			PredicateDigest:   hashText("core-template-predicate:" + service.ID + request.SubjectHash),
			SPDXVersion:       "SPDX-2.3", DocumentNamespace: "https://spdx.example/core/" + service.ID,
			EvidenceHash: hashText("core-template-sbom-evidence:" + service.ID + request.SubjectHash),
		})
	}
	evidence := make([]templates.GateEvidence, 0, len(templates.RequiredAdmissionGates()))
	for _, gate := range templates.RequiredAdmissionGates() {
		evidence = append(evidence, templates.GateEvidence{
			Gate: gate, Outcome: templates.EvidencePassed, SubjectHash: request.SubjectHash,
			Digest:    hashText("core-template-gate:" + string(gate) + request.SubjectHash),
			Reference: "urn:core-template:evidence:" + string(gate),
			Producer:  "core-template-authority", InvocationID: "core-" + string(gate), ObservedAt: now,
		})
	}
	signer := "core-template-authority"
	signature := templates.SignatureEnvelope{
		Format: "dsse", SubjectHash: request.SubjectHash, BundleDigest: signatureDigest,
		Signer: signer, TransparencyLogRef: "urn:rekor:core-template", SignedAt: now,
	}
	return templates.NewArtifactAuthorityReceipt(templates.NewArtifactAuthorityReceiptInput{
		ID: uuid.NewString(), SubjectHash: request.SubjectHash,
		SourceTreeHash: request.Candidate.Source.TreeHash, ArtifactDigest: artifactDigest,
		SBOMDigest: request.Candidate.SBOMDigest, SignatureBundleDigest: signatureDigest,
		PolicyHash:          hashText("core-template-policy"),
		Authority:           templates.ArtifactAuthorityIdentity{ID: "core-template-authority", Version: "v1"},
		VerifierImageDigest: hashText("core-template-verifier"), TrustRootDigest: hashText("core-template-trust-root"),
		TransparencyLog: templates.ArtifactTransparencyLog{
			ID: "core-template-log", EntryUUID: entryUUID, LogIndex: 1, IntegratedAt: integratedAt,
		},
		VerificationReference: "urn:core-template:receipt:" + request.SubjectHash,
		ArtifactDescriptor: templates.ArtifactDescriptor{
			Reference: "registry.example/core/template@" + artifactDigest,
			MediaType: "application/vnd.oci.image.manifest.v1+json", Digest: artifactDigest,
			SizeBytes: 256, Config: config, Layers: []templates.ArtifactBlobDescriptor{layer},
			TotalBytes: 256 + config.SizeBytes + layer.SizeBytes,
		},
		SBOMDescriptor: templates.ArtifactSBOMDescriptor{
			SchemaVersion: "worksflow.template-sbom-aggregate/v1",
			Digest:        request.Candidate.SBOMDigest, ServiceCount: len(services), Services: services,
		},
		Proof: templates.ArtifactAuthorityProof{
			PayloadType: "application/vnd.in-toto+json", PredicateType: "https://slsa.dev/provenance/v1",
			PayloadDigest:         hashText("core-template-payload:" + request.SubjectHash),
			SignatureBundleDigest: signatureDigest, SignerIdentities: []string{signer},
			TransparencyBundleDigest: hashText("core-template-transparency:" + request.SubjectHash),
			LogID:                    "core-template-log", EntryUUID: entryUUID, LogIndex: 1, TreeSize: 2,
			RootHash:     hashText("core-template-root:" + request.SubjectHash),
			IntegratedAt: integratedAt, CheckpointSignedAt: checkpointAt,
		},
		Evidence: evidence, Signature: signature, VerifiedAt: now,
		RecordedBy: request.RecordedBy, CreatedAt: now,
	})
}

func admitCoreTemplateRelease(
	t *testing.T,
	database *gorm.DB,
	releaseID uuid.UUID,
	templateID, serviceKind string,
	requestedBy, evaluatedBy uuid.UUID,
) string {
	t.Helper()
	serviceID := serviceKind + "-core"
	if serviceKind == "web" {
		serviceID = "web-ui"
	}
	candidate := templates.AdmissionCandidate{
		Source: templates.TemplateSource{
			Repository: "https://example.test/templates.git", Branch: "main",
			Commit:   strings.Repeat(map[string]string{"api": "1", "web": "2"}[serviceKind], 40),
			TreeHash: hashText("tree:" + templateID),
		},
		Manifest: templates.TemplateManifest{
			SchemaVersion: templates.TemplateManifestSchemaVersion,
			TemplateID:    templateID, DisplayName: templateID, Version: "1.0.0",
			Services: []templates.TemplateService{{ID: serviceID, Kind: serviceKind, RootPath: "."}},
			Toolchains: []templates.Toolchain{{
				Name: "runtime", Version: "1.0.0", Image: "ghcr.io/worksflow/runtime@" + hashText(templateID+":runtime"),
			}},
			Commands: map[string]templates.Command{
				"install":   {WorkingDirectory: ".", Argv: []string{"pnpm", "install", "--frozen-lockfile"}},
				"lint":      {WorkingDirectory: ".", Argv: []string{"pnpm", "lint"}},
				"typecheck": {WorkingDirectory: ".", Argv: []string{"pnpm", "typecheck"}},
				"test":      {WorkingDirectory: ".", Argv: []string{"pnpm", "test"}},
				"build":     {WorkingDirectory: ".", Argv: []string{"pnpm", "build"}},
				"start":     {WorkingDirectory: ".", Argv: []string{"pnpm", "start"}},
			},
			Ports:          []templates.Port{{Name: serviceKind + "-http", ServiceID: serviceID, Number: 3000, Protocol: "http", Exposure: "preview"}},
			HealthChecks:   []templates.HealthCheck{{ID: serviceKind + "-health", ServiceID: serviceID, PortName: serviceKind + "-http", Path: "/health"}},
			BuildOutputs:   []templates.BuildOutput{{ServiceID: serviceID, Path: "dist"}},
			ExtensionPaths: []string{"src"}, ProtectedPaths: []string{"protected"},
			EnvironmentSchema: []templates.EnvironmentVariable{{Name: "PORT", Required: true, Description: "service port"}},
			Lockfiles:         []templates.Lockfile{{Path: "pnpm-lock.yaml", Digest: hashText(templateID + ":lock"), Registry: "https://registry.npmjs.org"}},
			ProfileDigest:     hashText(templateID + ":profile"),
		},
		SBOMDigest: hashText("sbom:" + templateID), LicenseExpression: "Apache-2.0",
		LicenseDigest: hashText("license:" + templateID),
	}
	writer, err := templates.NewWriter(database, coreTemplateArtifactAuthorityFake{})
	if err != nil {
		t.Fatal(err)
	}
	registration, err := writer.Admit(context.Background(), templates.AdmitInput{
		AttemptID: uuid.NewString(), ReleaseID: releaseID.String(), Candidate: candidate,
		Bundle: templates.ArtifactAdmissionBundle{
			ArtifactReference: "registry.example/core/input@" + hashText(templateID+":input"),
			ServiceSBOMs: []templates.ArtifactServiceSBOMReference{{
				ServiceID:         serviceID,
				ImageReference:    "registry.example/core/" + serviceID + "@" + hashText(templateID+":image"),
				ReferrerReference: "registry.example/core/" + serviceID + "-sbom@" + hashText(templateID+":referrer"),
			}},
			DSSEEnvelope:          json.RawMessage(`{"payload":"test"}`),
			TransparencyBundle:    json.RawMessage(`{"entry":"test"}`),
			VerificationReference: "urn:core-template:input:" + templateID,
		},
		RequestedBy: requestedBy.String(), EvaluatedBy: evaluatedBy.String(),
	})
	if err != nil {
		t.Fatalf("admit exact v2 %s TemplateRelease: %v", serviceKind, err)
	}
	if registration.Release == nil || !registration.Release.Policy.AllowsNewProjects() {
		t.Fatalf("v2 %s TemplateRelease is not selectable", serviceKind)
	}
	return registration.Release.Release.ContentHash()
}
