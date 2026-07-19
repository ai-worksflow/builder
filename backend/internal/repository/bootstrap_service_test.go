package repository

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/storage/content"
	"github.com/worksflow/builder/backend/internal/templates"
)

type bootstrapAccessFake struct{ denied error }

func (access bootstrapAccessFake) RequireProjectEdit(context.Context, string, string) error {
	return access.denied
}

func (access bootstrapAccessFake) RequireProjectView(context.Context, string, string) error {
	return access.denied
}

type bootstrapContractGateFake struct {
	err        error
	selections []BootstrapBuildContractSelection
}

func (gate *bootstrapContractGateFake) RequireReadyForBootstrap(
	_ context.Context,
	_, _, _ string,
	selection BootstrapBuildContractSelection,
) error {
	gate.selections = append(gate.selections, selection)
	return gate.err
}

type bootstrapContentFake struct{ stored content.StoredContent }

func (reader bootstrapContentFake) Get(_ context.Context, id, hash string) (content.StoredContent, error) {
	if reader.stored.ID != id || reader.stored.ContentHash != hash {
		return content.StoredContent{}, content.ErrContentNotFound
	}
	return reader.stored, nil
}

type bootstrapFileWriterFake struct {
	puts      []string
	files     map[string][]byte
	pointers  map[string]FileBlobPointer
	settles   []string
	settleErr error
}

func (writer *bootstrapFileWriterFake) Put(
	_ context.Context,
	projectID, _ string,
	value []byte,
) (FileBlobWriteResult, error) {
	writer.puts = append(writer.puts, string(value))
	digest := sha256.Sum256(value)
	hash := fmt.Sprintf("sha256:%x", digest[:])
	objectDigest := sha256.Sum256(append([]byte("object:"), value...))
	pointer := FileBlobPointer{
		Store: FileContentStore, Ref: "file-" + hash,
		OwnerID: uuid.NewString(), ContentHash: hash, ByteSize: int64(len(value)),
		ContentObjectHash: fmt.Sprintf("sha256:%x", objectDigest[:]),
	}
	if writer.files == nil {
		writer.files = make(map[string][]byte)
		writer.pointers = make(map[string]FileBlobPointer)
	}
	key := fmt.Sprintf("%s:%d", hash, len(value))
	writer.files[key] = append([]byte(nil), value...)
	writer.pointers[key] = pointer
	return FileBlobWriteResult{Pointer: pointer}, nil
}

func (writer *bootstrapFileWriterFake) Resolve(
	_ context.Context,
	_ string,
	contentHash string,
	byteSize int64,
) (FileBlobPointer, []byte, error) {
	key := fmt.Sprintf("%s:%d", contentHash, byteSize)
	value, found := writer.files[key]
	if !found {
		return FileBlobPointer{}, nil, ErrFileBlobNotFound
	}
	return writer.pointers[key], append([]byte(nil), value...), nil
}

func (writer *bootstrapFileWriterFake) Settle(
	ctx context.Context,
	projectID, contentHash string,
	byteSize int64,
) error {
	writer.settles = append(writer.settles, contentHash)
	if writer.settleErr != nil {
		return writer.settleErr
	}
	_, _, err := writer.Resolve(ctx, projectID, contentHash, byteSize)
	return err
}

type bootstrapTemplateSourceFake struct {
	requests []TemplateSourceRequest
	files    []TemplateSourceFile
}

type bootstrapArtifactAuthorityFake struct{}

func (bootstrapArtifactAuthorityFake) Readiness(context.Context) error { return nil }

func (bootstrapArtifactAuthorityFake) Verify(
	_ context.Context,
	request templates.ArtifactAuthorityVerifyRequest,
) (templates.ArtifactAuthorityReceipt, error) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	integratedAt := now.Add(-2 * time.Second)
	checkpointSignedAt := now.Add(-time.Second)
	artifactDigest := digestFixture("bootstrap-authority-artifact:" + request.SubjectHash)
	signatureBundleDigest := digestFixture("bootstrap-authority-signature:" + request.SubjectHash)
	transparencyBundleDigest := digestFixture("bootstrap-authority-transparency:" + request.SubjectHash)
	config := templates.ArtifactBlobDescriptor{
		MediaType: "application/vnd.oci.image.config.v1+json",
		Digest:    digestFixture("bootstrap-authority-config:" + request.SubjectHash), SizeBytes: 64,
	}
	layer := templates.ArtifactBlobDescriptor{
		MediaType: "application/vnd.oci.image.layer.v1.tar+gzip",
		Digest:    digestFixture("bootstrap-authority-layer:" + request.SubjectHash), SizeBytes: 128,
	}
	services := make([]templates.ArtifactSBOMServiceDescriptor, 0, len(request.Candidate.Manifest.Services))
	for _, service := range request.Candidate.Manifest.Services {
		imageDigest := digestFixture("bootstrap-authority-image:" + service.ID + ":" + request.SubjectHash)
		referrerDigest := digestFixture("bootstrap-authority-referrer:" + service.ID + ":" + request.SubjectHash)
		services = append(services, templates.ArtifactSBOMServiceDescriptor{
			ServiceID:         service.ID,
			ImageReference:    "registry.example/bootstrap/" + service.ID + "@" + imageDigest,
			ImageDigest:       imageDigest,
			ReferrerReference: "registry.example/bootstrap/" + service.ID + "-sbom@" + referrerDigest,
			ReferrerDigest:    referrerDigest,
			StatementDigest:   digestFixture("bootstrap-authority-statement:" + service.ID + ":" + request.SubjectHash),
			PredicateDigest:   digestFixture("bootstrap-authority-predicate:" + service.ID + ":" + request.SubjectHash),
			SPDXVersion:       "SPDX-2.3", DocumentNamespace: "https://spdx.example/bootstrap/" + service.ID,
			EvidenceHash: digestFixture("bootstrap-authority-sbom-evidence:" + service.ID + ":" + request.SubjectHash),
		})
	}
	evidence := make([]templates.GateEvidence, 0, len(templates.RequiredAdmissionGates()))
	for _, gate := range templates.RequiredAdmissionGates() {
		evidence = append(evidence, templates.GateEvidence{
			Gate: gate, Outcome: templates.EvidencePassed, SubjectHash: request.SubjectHash,
			Digest:    digestFixture("bootstrap-authority-gate:" + string(gate) + ":" + request.SubjectHash),
			Reference: "urn:bootstrap-authority:evidence:" + string(gate),
			Producer:  "bootstrap-artifact-authority", InvocationID: "bootstrap-" + string(gate),
			ObservedAt: now,
		})
	}
	signer := "bootstrap-artifact-authority"
	signature := templates.SignatureEnvelope{
		Format: "dsse", SubjectHash: request.SubjectHash, BundleDigest: signatureBundleDigest,
		Signer: signer, TransparencyLogRef: "urn:rekor:bootstrap-entry", SignedAt: now,
	}
	entryUUID := "entry:" + strings.ReplaceAll(uuid.NewString(), "-", "")
	return templates.NewArtifactAuthorityReceipt(templates.NewArtifactAuthorityReceiptInput{
		ID: uuid.NewString(), SubjectHash: request.SubjectHash,
		SourceTreeHash: request.Candidate.Source.TreeHash, ArtifactDigest: artifactDigest,
		SBOMDigest: request.Candidate.SBOMDigest, SignatureBundleDigest: signatureBundleDigest,
		PolicyHash:          digestFixture("bootstrap-authority-policy"),
		Authority:           templates.ArtifactAuthorityIdentity{ID: "bootstrap-artifact-authority", Version: "v1"},
		VerifierImageDigest: digestFixture("bootstrap-authority-verifier"),
		TrustRootDigest:     digestFixture("bootstrap-authority-trust-root"),
		TransparencyLog: templates.ArtifactTransparencyLog{
			ID: "bootstrap-transparency-log", EntryUUID: entryUUID, LogIndex: 1, IntegratedAt: integratedAt,
		},
		VerificationReference: "urn:bootstrap-authority:receipt:" + request.SubjectHash,
		ArtifactDescriptor: templates.ArtifactDescriptor{
			Reference: "registry.example/bootstrap/template@" + artifactDigest,
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
			PayloadDigest:         digestFixture("bootstrap-authority-payload:" + request.SubjectHash),
			SignatureBundleDigest: signatureBundleDigest, SignerIdentities: []string{signer},
			TransparencyBundleDigest: transparencyBundleDigest,
			LogID:                    "bootstrap-transparency-log", EntryUUID: entryUUID, LogIndex: 1, TreeSize: 2,
			RootHash:     digestFixture("bootstrap-authority-root:" + request.SubjectHash),
			IntegratedAt: integratedAt, CheckpointSignedAt: checkpointSignedAt,
		},
		Evidence: evidence, Signature: signature, VerifiedAt: now,
		RecordedBy: request.RecordedBy, CreatedAt: now,
	})
}

func admitBootstrapTemplateRelease(
	t *testing.T,
	fixture *candidateStorePostgresFixture,
	templateID, serviceKind string,
	requestedBy, evaluatedBy uuid.UUID,
) (uuid.UUID, string) {
	t.Helper()
	serviceID := serviceKind
	extensionPath, protectedPath := "src", "package.json"
	if serviceKind == "api" {
		extensionPath, protectedPath = "app", "pyproject.toml"
	}
	candidate := templates.AdmissionCandidate{
		Source: templates.TemplateSource{
			Repository: "https://github.com/example/templates.git", Branch: serviceKind,
			Commit:   strings.Repeat(map[string]string{"web": "1", "api": "2"}[serviceKind], 40),
			TreeHash: digestFixture(serviceKind + "-tree"),
		},
		Manifest: templates.TemplateManifest{
			SchemaVersion: templates.TemplateManifestSchemaVersion,
			TemplateID:    templateID, DisplayName: templateID, Version: "1.0.0",
			Services: []templates.TemplateService{{ID: serviceID, Kind: serviceKind, RootPath: "."}},
			Toolchains: []templates.Toolchain{{
				Name: "runtime", Version: "1.0.0",
				Image: "ghcr.io/worksflow/runtime@" + digestFixture(serviceKind+"-runtime"),
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
			ExtensionPaths: []string{extensionPath}, ProtectedPaths: []string{protectedPath},
			EnvironmentSchema: []templates.EnvironmentVariable{{Name: "PORT", Required: true, Description: "service port"}},
			Lockfiles:         []templates.Lockfile{{Path: "pnpm-lock.yaml", Digest: digestFixture(serviceKind + "-lock"), Registry: "https://registry.npmjs.org"}},
			ProfileDigest:     digestFixture(serviceKind + "-profile"),
		},
		SBOMDigest:        digestFixture(serviceKind + "-sbom"),
		LicenseExpression: "Apache-2.0", LicenseDigest: digestFixture(serviceKind + "-license"),
	}
	writer, err := templates.NewWriter(fixture.gorm, bootstrapArtifactAuthorityFake{})
	if err != nil {
		t.Fatal(err)
	}
	releaseID := uuid.New()
	registration, err := writer.Admit(fixture.context, templates.AdmitInput{
		AttemptID: uuid.NewString(), ReleaseID: releaseID.String(), Candidate: candidate,
		Bundle: templates.ArtifactAdmissionBundle{
			ArtifactReference: "registry.example/bootstrap/input@" + digestFixture(serviceKind+"-input"),
			ServiceSBOMs: []templates.ArtifactServiceSBOMReference{{
				ServiceID:         serviceID,
				ImageReference:    "registry.example/bootstrap/" + serviceID + "@" + digestFixture(serviceKind+"-image-input"),
				ReferrerReference: "registry.example/bootstrap/" + serviceID + "-sbom@" + digestFixture(serviceKind+"-sbom-input"),
			}},
			DSSEEnvelope:          json.RawMessage(`{"payload":"test"}`),
			TransparencyBundle:    json.RawMessage(`{"entry":"test"}`),
			VerificationReference: "urn:bootstrap-authority:input:" + serviceKind,
		},
		RequestedBy: requestedBy.String(), EvaluatedBy: evaluatedBy.String(),
	})
	if err != nil {
		t.Fatalf("admit exact v2 %s TemplateRelease: %v", serviceKind, err)
	}
	if registration.Release == nil || !registration.Release.Policy.AllowsNewProjects() {
		t.Fatalf("v2 %s TemplateRelease is not selectable", serviceKind)
	}
	return releaseID, registration.Release.Release.ContentHash()
}

func (materializer *bootstrapTemplateSourceFake) Materialize(
	_ context.Context,
	request TemplateSourceRequest,
) ([]TemplateSourceFile, error) {
	materializer.requests = append(materializer.requests, request)
	return validateTemplateSourceFiles(materializer.files)
}

func TestCandidateBootstrapConsumedManifestPostgresCreatesExactIdempotentCandidate(t *testing.T) {
	fixture := openCandidateStorePostgresFixture(t)
	now := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	manifestID, contractID, stackID := uuid.New(), uuid.New(), uuid.New()
	workspaceArtifactID, workspaceRevisionID, proposalID := uuid.New(), uuid.New(), uuid.New()
	manifestHash := strings.Repeat("a", 64)
	contractHash := strings.Repeat("b", 64)
	stackHash := "sha256:" + strings.Repeat("c", 64)
	workspacePayload, err := domain.CanonicalJSON(map[string]any{
		"schemaVersion": 1,
		"id":            "workspace-main",
		"files": []map[string]any{
			{"path": "apps/web/src/main.tsx", "content": "export const ready = true\n", "language": "typescript"},
			{"path": "services/api/main.py", "content": "print('ready')\n", "language": "python"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	workspaceHashDigest := sha256.Sum256(workspacePayload)
	workspaceHash := fmt.Sprintf("sha256:%x", workspaceHashDigest[:])
	reviewerID := uuid.New()
	if _, err := fixture.database.ExecContext(fixture.context, `
INSERT INTO users (id, email, display_name, password_hash)
VALUES ($1, $2, 'Bootstrap Template reviewer', 'not-used')
`, reviewerID, "bootstrap-template-reviewer-"+uuid.NewString()+"@example.com"); err != nil {
		t.Fatal(err)
	}
	webReleaseID, webReleaseHash := admitBootstrapTemplateRelease(
		t, fixture, "golden-web", "web", fixture.actorID, reviewerID,
	)
	apiReleaseID, apiReleaseHash := admitBootstrapTemplateRelease(
		t, fixture, "golden-api", "api", fixture.actorID, reviewerID,
	)

	transaction, err := fixture.database.BeginTx(fixture.context, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(fixture.context, `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatal(err)
	}
	stackDocument, err := json.Marshal(map[string]any{
		"id": stackID.String(), "schemaVersion": "full-stack-template/v1",
		"templateId": "golden-stack", "version": "1.0.0", "contentHash": stackHash,
		"createdBy": fixture.actorID.String(), "createdAt": now.Format(time.RFC3339Nano),
		"components": []map[string]any{
			{"role": "web", "mountPath": "apps/web", "release": map[string]any{"id": webReleaseID.String(), "contentHash": webReleaseHash}},
			{"role": "api", "mountPath": "services/api", "release": map[string]any{"id": apiReleaseID.String(), "contentHash": apiReleaseHash}},
		},
		"layout": map[string]any{"contractTruthSource": "openapi"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transaction.ExecContext(fixture.context, `
INSERT INTO full_stack_template_releases (
  id, schema_version, template_id, release_version, document, content_hash, created_by, created_at
) VALUES ($1, 'full-stack-template/v1', 'golden-stack', '1.0.0', $2::jsonb, $3, $4, $5)
`, stackID, stackDocument, stackHash, fixture.actorID, now); err != nil {
		t.Fatalf("seed FullStackTemplate: %v", err)
	}
	if _, err := transaction.ExecContext(fixture.context, `
INSERT INTO full_stack_template_components (
  full_stack_template_id, full_stack_content_hash, role, mount_path,
  template_release_id, template_release_content_hash
) VALUES
  ($1, $2, 'web', 'apps/web', $3, $4),
  ($1, $2, 'api', 'services/api', $5, $6)
`, stackID, stackHash, webReleaseID, webReleaseHash, apiReleaseID, apiReleaseHash); err != nil {
		t.Fatalf("seed FullStackTemplate components: %v", err)
	}
	if _, err := transaction.ExecContext(fixture.context, `
INSERT INTO application_build_manifests (
  id, project_id, root_manifest_id, schema_version, content_store, content_ref,
  content_hash, manifest_hash, status, created_by, created_at
) VALUES ($1, $2, $1, 1, 'mongo', $3, $4, $5, 'consumed', $6, $7)
`, manifestID, fixture.projectID, "manifest-content-"+manifestID.String(), digestFixture("manifest-content"), manifestHash, fixture.actorID, now); err != nil {
		t.Fatalf("seed consumed BuildManifest: %v", err)
	}
	if _, err := transaction.ExecContext(fixture.context, `
INSERT INTO application_build_contracts (
  id, project_id, build_manifest_id, build_manifest_hash,
  full_stack_template_id, full_stack_template_hash,
  schema_version, compiler_version, compiler_hash,
  content_store, content_ref, content_hash, contract_hash, status,
  must_count, must_ready_count, obligation_count, source_count,
  template_release_count, blocking_count, conflict_count, version,
  created_by, created_at
) VALUES (
  $1, $2, $3, $4, $5, $6,
  'application-build-contract/v2', 'test', $7,
  'mongo', $8, $9, $10, 'ready',
  1, 1, 1, 1, 2, 0, 0, 1, $11, $12
)
`, contractID, fixture.projectID, manifestID, manifestHash, stackID, stackHash,
		digestFixture("compiler"), "contract-content-"+contractID.String(), digestFixture("contract-content"), contractHash,
		fixture.actorID, now); err != nil {
		t.Fatalf("seed BuildContract: %v", err)
	}
	if _, err := transaction.ExecContext(fixture.context, `
INSERT INTO application_build_contract_template_releases (
  contract_id, ordinal, role, template_release_id, template_release_content_hash
) VALUES
  ($1, 0, 'web', $2, $3),
  ($1, 1, 'api', $4, $5)
`, contractID, webReleaseID, webReleaseHash, apiReleaseID, apiReleaseHash); err != nil {
		t.Fatalf("seed BuildContract releases: %v", err)
	}
	if _, err := transaction.ExecContext(fixture.context, `
INSERT INTO artifacts (
  id, project_id, kind, artifact_key, title, lifecycle, version,
  latest_revision_id, latest_approved_revision_id, created_by, created_at, updated_at
) VALUES ($1, $2, 'workspace', 'WORKSPACE-MAIN', 'Workspace', 'active', 1, $3, $3, $4, $5, $5)
`, workspaceArtifactID, fixture.projectID, workspaceRevisionID, fixture.actorID, now); err != nil {
		t.Fatalf("seed Workspace artifact: %v", err)
	}
	if _, err := transaction.ExecContext(fixture.context, `
INSERT INTO implementation_proposals (
  id, project_id, build_manifest_id, application_build_contract_id, application_build_contract_hash,
  status, version, content_store, content_ref, content_hash, payload_hash,
  operation_count, accepted_count, rejected_count, created_by, created_at, applied_by, applied_at
) VALUES ($1, $2, $3, $4, $5, 'applied', 2, 'mongo', $6, $7, $8, 2, 2, 0, $9, $10, $9, $10)
`, proposalID, fixture.projectID, manifestID, contractID, contractHash,
		"proposal-content-"+proposalID.String(), digestFixture("proposal-content"), digestFixture("proposal-payload"),
		fixture.actorID, now); err != nil {
		t.Fatalf("seed applied ImplementationProposal: %v", err)
	}
	if _, err := transaction.ExecContext(fixture.context, `
INSERT INTO artifact_revisions (
  id, artifact_id, revision_number, schema_version, content_store, content_ref,
  content_hash, byte_size, workflow_status, change_source, change_summary,
  implementation_proposal_id, created_by, created_at, approved_at
) VALUES ($1, $2, 1, 1, 'mongo', $3, $4, $5, 'approved', 'ai_proposal', 'applied', $6, $7, $8, $8)
`, workspaceRevisionID, workspaceArtifactID, "workspace-content-"+workspaceRevisionID.String(), workspaceHash,
		len(workspacePayload), proposalID, fixture.actorID, now); err != nil {
		t.Fatalf("seed applied WorkspaceRevision: %v", err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatal(err)
	}

	stored := content.StoredContent{
		Reference: content.Reference{
			ID: "workspace-content-" + workspaceRevisionID.String(), ContentHash: workspaceHash,
			ByteSize: int64(len(workspacePayload)), SchemaVersion: 1,
		},
		ProjectID: fixture.projectID.String(), AggregateType: "artifact_revision",
		AggregateID: workspaceRevisionID.String(), State: content.StateFinalized,
		Payload: workspacePayload, CreatedAt: now,
	}
	treeContents := newFakeTreeContentStore()
	trees, err := NewTreeStore(treeContents)
	if err != nil {
		t.Fatal(err)
	}
	candidates, err := NewGORMCandidateStore(fixture.gorm, trees)
	if err != nil {
		t.Fatal(err)
	}
	files := &bootstrapFileWriterFake{}
	contractGate := &bootstrapContractGateFake{}
	templateSources := &bootstrapTemplateSourceFake{files: []TemplateSourceFile{
		{Path: "apps/web/package.json", Mode: "100644", Content: []byte(`{"scripts":{"dev":"vite"}}`)},
		{Path: "services/api/main.py", Mode: "100644", Content: []byte("print('template')\n")},
		{Path: "templates.lock.json", Mode: "100644", Content: []byte(`{"schemaVersion":"template-source-lock/v1"}`)},
	}}
	service, err := NewCandidateBootstrapService(
		fixture.gorm, bootstrapContentFake{stored: stored}, files, trees, candidates,
		bootstrapAccessFake{}, contractGate, func() time.Time { return now.Add(time.Minute) },
		WithTemplateSourceMaterializer(templateSources),
	)
	if err != nil {
		t.Fatal(err)
	}
	input := BootstrapCandidateInput{
		ProjectID: fixture.projectID.String(), BuildManifestID: manifestID.String(),
		ActorID: fixture.actorID.String(), OperationID: "bootstrap-consumed-1",
	}
	created, err := service.Bootstrap(fixture.context, input)
	if err != nil {
		t.Fatalf("bootstrap Candidate: %v", err)
	}
	if !created.Created || created.Recovered || created.FinalizationPending ||
		created.RepositorySnapshotReceipt.ContentHash == "" ||
		created.Candidate.BuildManifest != (ExactReference{ID: manifestID.String(), ContentHash: manifestHash}) ||
		created.Candidate.BuildContract != (ExactReference{ID: contractID.String(), ContentHash: contractHash}) ||
		created.Candidate.FullStackTemplate != (ExactReference{ID: stackID.String(), ContentHash: stackHash}) ||
		created.Candidate.BaseWorkspaceRevision == nil ||
		created.Candidate.BaseWorkspaceRevision.RevisionID != workspaceRevisionID.String() ||
		len(created.Candidate.CurrentTree.Files) != 2 {
		t.Fatalf("created Candidate lost exact source facts: %#v", created)
	}
	if len(contractGate.selections) != 1 || contractGate.selections[0] != (BootstrapBuildContractSelection{
		ID: contractID.String(), ContractHash: contractHash,
	}) {
		t.Fatalf("bootstrap bypassed exact Constructor readiness authority: %#v", contractGate.selections)
	}
	if len(files.puts) != 2 || files.puts[0] != "export const ready = true\n" || files.puts[1] != "print('ready')\n" {
		t.Fatalf("WorkspaceRevision files were not deterministically materialized: %#v", files.puts)
	}
	loadedReceipt, err := service.GetSnapshot(
		fixture.context, fixture.projectID.String(), created.Candidate.RepositorySnapshotID,
		created.RepositorySnapshotReceipt.ContentHash, fixture.actorID.String(),
	)
	if err != nil {
		t.Fatalf("read exact RepositorySnapshot receipt: %v", err)
	}
	equalReceipt, err := sameRepositorySnapshotReceipt(created.RepositorySnapshotReceipt, loadedReceipt)
	if err != nil || !equalReceipt {
		t.Fatalf("exact RepositorySnapshot receipt changed: equal=%v err=%v", equalReceipt, err)
	}
	if _, err := service.GetSnapshot(
		fixture.context, fixture.projectID.String(), created.Candidate.RepositorySnapshotID,
		digestFixture("wrong-repository-snapshot-receipt"), fixture.actorID.String(),
	); !errors.Is(err, ErrRepositorySnapshotDrift) {
		t.Fatalf("wrong RepositorySnapshot content hash error = %v", err)
	}
	searched, err := service.SearchCandidate(fixture.context, CandidateSearchInput{
		ProjectID: fixture.projectID.String(), CandidateID: created.Candidate.ID,
		ExpectedHeadGeneration: created.Candidate.Version,
		ExpectedRootHash:       created.Candidate.CurrentTree.TreeHash,
		Query:                  "ready", CaseSensitive: true, ActorID: fixture.actorID.String(),
	})
	if err != nil || searched.Truncated || len(searched.Matches) != 2 ||
		searched.Matches[0].Path != "apps/web/src/main.tsx" || searched.Matches[0].Line != 1 ||
		searched.Matches[0].ContentHash != created.Candidate.CurrentTree.Files[0].ContentHash ||
		searched.Matches[1].Path != "services/api/main.py" || searched.Matches[1].Line != 1 ||
		searched.Head.Generation != created.Candidate.Version ||
		searched.Head.RootHash != created.Candidate.CurrentTree.TreeHash {
		t.Fatalf("real PostgreSQL Candidate exact-tree search = %#v err=%v", searched, err)
	}

	replayed, err := service.Bootstrap(fixture.context, input)
	if err != nil {
		t.Fatalf("replay bootstrap Candidate: %v", err)
	}
	if replayed.Created || !replayed.Recovered || replayed.Candidate.ID != created.Candidate.ID ||
		replayed.RepositorySnapshotReceipt.ContentHash != created.RepositorySnapshotReceipt.ContentHash || len(files.puts) != 2 {
		t.Fatalf("bootstrap replay created another Candidate or rewrote files: %#v", replayed)
	}
	contractGate.err = errors.New("obsolete compiler or unavailable contract content")
	if _, err := service.Bootstrap(fixture.context, BootstrapCandidateInput{
		ProjectID: fixture.projectID.String(), BuildManifestID: manifestID.String(),
		ActorID: fixture.actorID.String(), OperationID: "bootstrap-consumed-obsolete-contract",
	}); !errors.Is(err, ErrBootstrapNotReady) {
		t.Fatalf("Constructor readiness denial error = %v", err)
	}
	contractGate.err = nil
	if len(files.puts) != 2 {
		t.Fatalf("readiness-denied bootstrap wrote files: %#v", files.puts)
	}
	heads, err := service.ListHeads(
		fixture.context, fixture.projectID.String(), fixture.actorID.String(),
	)
	if err != nil || heads.SchemaVersion != "repository-candidate-head-list/v1" ||
		len(heads.Candidates) != 1 || heads.Candidates[0].Candidate.ID != created.Candidate.ID ||
		heads.Candidates[0].RebaseID != "" {
		t.Fatalf("durable Candidate head discovery = %#v err=%v", heads, err)
	}
	var snapshots, candidateRows, receiptRows int
	if err := fixture.database.QueryRowContext(fixture.context, `
SELECT
  (SELECT count(*) FROM repository_snapshots WHERE project_id = $1 AND build_manifest_id = $2),
  (SELECT count(*) FROM candidate_workspaces WHERE project_id = $1 AND build_manifest_id = $2),
  (SELECT count(*) FROM repository_snapshot_receipts WHERE project_id = $1)
`, fixture.projectID, manifestID).Scan(&snapshots, &candidateRows, &receiptRows); err != nil {
		t.Fatal(err)
	}
	if snapshots != 1 || candidateRows != 1 || receiptRows != 1 {
		t.Fatalf("bootstrap row counts snapshot=%d candidate=%d receipt=%d", snapshots, candidateRows, receiptRows)
	}

	templateManifestID, templateContractID := uuid.New(), uuid.New()
	templateManifestHash := strings.Repeat("d", 64)
	templateContractHash := strings.Repeat("e", 64)
	templateSeed, err := fixture.database.BeginTx(fixture.context, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer templateSeed.Rollback()
	if _, err := templateSeed.ExecContext(fixture.context, `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatal(err)
	}
	if _, err := templateSeed.ExecContext(fixture.context, `
INSERT INTO application_build_manifests (
  id, project_id, root_manifest_id, schema_version, content_store, content_ref,
  content_hash, manifest_hash, status, created_by, created_at
) VALUES ($1, $2, $1, 1, 'mongo', $3, $4, $5, 'frozen', $6, $7)
`, templateManifestID, fixture.projectID, "manifest-content-"+templateManifestID.String(),
		digestFixture("template-manifest-content"), templateManifestHash, fixture.actorID, now); err != nil {
		t.Fatalf("seed template-only BuildManifest: %v", err)
	}
	if _, err := templateSeed.ExecContext(fixture.context, `
INSERT INTO application_build_contracts (
  id, project_id, build_manifest_id, build_manifest_hash,
  full_stack_template_id, full_stack_template_hash,
  schema_version, compiler_version, compiler_hash,
  content_store, content_ref, content_hash, contract_hash, status,
  must_count, must_ready_count, obligation_count, source_count,
  template_release_count, blocking_count, conflict_count, version,
  created_by, created_at
) VALUES (
  $1, $2, $3, $4, $5, $6,
  'application-build-contract/v2', 'test', $7,
  'mongo', $8, $9, $10, 'ready',
  1, 1, 1, 1, 2, 0, 0, 1, $11, $12
)
`, templateContractID, fixture.projectID, templateManifestID, templateManifestHash, stackID, stackHash,
		digestFixture("template-compiler"), "contract-content-"+templateContractID.String(),
		digestFixture("template-contract-content"), templateContractHash, fixture.actorID, now); err != nil {
		t.Fatalf("seed template-only BuildContract: %v", err)
	}
	if _, err := templateSeed.ExecContext(fixture.context, `
INSERT INTO application_build_contract_template_releases (
  contract_id, ordinal, role, template_release_id, template_release_content_hash
) VALUES
  ($1, 0, 'web', $2, $3),
  ($1, 1, 'api', $4, $5)
`, templateContractID, webReleaseID, webReleaseHash, apiReleaseID, apiReleaseHash); err != nil {
		t.Fatalf("seed template-only exact releases: %v", err)
	}
	if err := templateSeed.Commit(); err != nil {
		t.Fatal(err)
	}

	templateCreated, err := service.Bootstrap(fixture.context, BootstrapCandidateInput{
		ProjectID: fixture.projectID.String(), BuildManifestID: templateManifestID.String(),
		ActorID: fixture.actorID.String(), OperationID: "bootstrap-template-source-1",
	})
	if err != nil {
		t.Fatalf("bootstrap exact TemplateRelease sources: %v", err)
	}
	if !templateCreated.Created || templateCreated.Candidate.BaseWorkspaceRevision != nil ||
		len(templateCreated.Candidate.CurrentTree.Files) != 3 || len(templateSources.requests) != 1 ||
		templateSources.requests[0].BuildContract.ID != templateContractID.String() ||
		len(templateSources.requests[0].Components) != 2 {
		t.Fatalf("template-only Candidate lost exact source lineage: %#v requests=%#v", templateCreated, templateSources.requests)
	}
	var baseRevision sql.NullString
	if err := fixture.database.QueryRowContext(fixture.context, `
SELECT base_workspace_revision_id::text
FROM repository_snapshots
WHERE id = $1 AND project_id = $2
`, templateCreated.Candidate.RepositorySnapshotID, fixture.projectID).Scan(&baseRevision); err != nil {
		t.Fatal(err)
	}
	if baseRevision.Valid {
		t.Fatalf("template-only RepositorySnapshot invented a WorkspaceRevision: %q", baseRevision.String)
	}
	heads, err = service.ListHeads(
		fixture.context, fixture.projectID.String(), fixture.actorID.String(),
	)
	if err != nil || len(heads.Candidates) != 2 {
		t.Fatalf("multiple durable Candidate heads were not exposed explicitly: %#v err=%v", heads, err)
	}
}

func TestDecodeBootstrapWorkspaceRejectsEmptyUnsafeAndCaseCollidingTrees(t *testing.T) {
	for name, payload := range map[string]string{
		"empty":          `{"files":[]}`,
		"unsafe":         `{"files":[{"path":"../secret","content":"x"}]}`,
		"case collision": `{"files":[{"path":"src/App.tsx","content":"a"},{"path":"src/app.tsx","content":"b"}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeBootstrapWorkspace(json.RawMessage(payload)); err == nil {
				t.Fatal("invalid workspace unexpectedly decoded")
			}
		})
	}
}
