package workflowinputauthority

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/worksflow/builder/backend/internal/canonicalreviewreceipt"
	"github.com/worksflow/builder/backend/internal/constructor"
	"github.com/worksflow/builder/backend/internal/core"
	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/qualificationevidence"
	qualificationpolicy "github.com/worksflow/builder/backend/internal/qualificationpolicyauthority"
	"github.com/worksflow/builder/backend/internal/qualificationreceipt"
	templatecatalog "github.com/worksflow/builder/backend/internal/templates"
	workflowruntime "github.com/worksflow/builder/backend/internal/workflow"
	"github.com/worksflow/builder/backend/migrations"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

const (
	postgresStoreCanaryFullStackTemplateID = "23232323-2323-4323-8323-232323232323"
	postgresStoreCanaryTemplateEvaluatorID = "24242424-2424-4424-8424-242424242424"
)

// This canary crosses the actual production boundary in both directions:
// PostgresStore compiles a server-resolved Candidate, migration 78 authors the
// immutable aggregate, and the production recovery decoder rebuilds and
// validates every retained byte sequence before the activation transaction is
// allowed to commit.
func TestPostgresStoreFreezeProductionRecoveryCanary(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := "workflow_input_store_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := base.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = base.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`)
	})

	database, err := sql.Open("pgx", postgresStoreCanaryDSN(t, dsn, schema))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if err := migrations.Up(ctx, database); err != nil {
		t.Fatalf("apply migrations in temporary schema: %v", err)
	}

	candidate := postgresStoreCanaryCandidate(t)
	candidate = bindPostgresStoreCanaryTemplate(t, ctx, database, candidate)
	seedPostgresStoreCanary(t, ctx, database, candidate)
	candidate = bindPostgresStoreCanaryPolicy(t, ctx, database, candidate)
	proposed, err := Compile(candidate)
	if err != nil {
		t.Fatalf("compile PostgreSQL canary Candidate: %v", err)
	}

	store, err := NewPostgresStore(database)
	if err != nil {
		t.Fatal(err)
	}
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	postgresTransaction, err := NewPostgresTransaction(transaction)
	if err != nil {
		_ = transaction.Rollback()
		t.Fatal(err)
	}
	recovered, err := store.Freeze(ctx, postgresTransaction, candidate)
	if err != nil {
		_ = transaction.Rollback()
		t.Fatalf("freeze through PostgresStore: %v", err)
	}
	if recovered.Idempotent {
		_ = transaction.Rollback()
		t.Fatal("first PostgresStore freeze was reported as an idempotent replay")
	}
	if err := ValidateRecord(recovered); err != nil {
		_ = transaction.Rollback()
		t.Fatalf("validate production-decoded frozen record: %v", err)
	}
	if !sameImmutableRecord(recovered, proposed) {
		_ = transaction.Rollback()
		t.Fatal("production recovery decoder did not reproduce the compiled Candidate")
	}
	if err := activatePostgresStoreCanary(ctx, transaction, recovered); err != nil {
		_ = transaction.Rollback()
		t.Fatalf("close Workflow Input activation transaction: %v", err)
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("commit PostgresStore Workflow Input activation: %v", err)
	}

	resolved, err := store.ResolveNode(ctx, recovered.WorkflowRunID, recovered.NodeRunID)
	if err != nil {
		t.Fatalf("resolve committed node authority through production decoder: %v", err)
	}
	if err := ValidateRecord(resolved); err != nil {
		t.Fatalf("validate committed production-decoded record: %v", err)
	}
	if !sameImmutableRecord(resolved, proposed) {
		t.Fatal("committed recovery bundle differs from the compiled Candidate")
	}

	replayTransaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	replayToken, err := NewPostgresTransaction(replayTransaction)
	if err != nil {
		_ = replayTransaction.Rollback()
		t.Fatal(err)
	}
	replayed, err := store.Freeze(ctx, replayToken, candidate)
	if err != nil {
		_ = replayTransaction.Rollback()
		t.Fatalf("exact PostgresStore replay: %v", err)
	}
	if !replayed.Idempotent || !sameImmutableRecord(replayed, proposed) {
		_ = replayTransaction.Rollback()
		t.Fatal("exact PostgresStore replay did not return the same immutable authority")
	}
	if err := replayTransaction.Rollback(); err != nil {
		t.Fatal(err)
	}
}

func postgresStoreCanaryCandidate(t *testing.T) Candidate {
	t.Helper()
	candidate := goldenCandidate(t)
	now := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	seeded, err := workflowruntime.MinimumLoopDefinition(
		candidate.Input.Definition.DefinitionID,
		candidate.Input.Run.StartedBy,
		now,
	)
	if err != nil {
		t.Fatalf("build PostgreSQL canary production path: %v", err)
	}
	nodes := append([]domain.NodeDefinition(nil), seeded.Nodes...)
	var gateSchema json.RawMessage
	for index := range nodes {
		if nodes[index].ID != "quality" {
			continue
		}
		nodes[index].ID = "release-quality"
		nodes[index].Name = "Release quality"
		gateSchema = append(json.RawMessage(nil), nodes[index].OutputSchema...)
	}
	if len(gateSchema) == 0 {
		t.Fatal("PostgreSQL canary production path lacks its release quality gate")
	}
	external := domain.ExactExternalQualificationGateConfig()
	nodes = append(nodes, domain.NodeDefinition{
		ID: ExternalQualificationGate, Name: "External qualification", Type: domain.NodeExternalQualificationGate,
		InputSchema: gateSchema, OutputSchema: gateSchema, ExternalQualificationGate: &external,
	})
	edges := make([]domain.WorkflowEdge, 0, len(seeded.Edges)+1)
	for _, edge := range seeded.Edges {
		if edge.From == "quality" {
			edge.From = "release-quality"
		}
		if edge.To == "quality" {
			edge.To = "release-quality"
		}
		if edge.From == "release-quality" && edge.To == "publish" {
			edges = append(edges,
				domain.WorkflowEdge{ID: "quality-external", From: "release-quality", To: ExternalQualificationGate},
				domain.WorkflowEdge{ID: "external-publish", From: ExternalQualificationGate, To: "publish"},
			)
			continue
		}
		edges = append(edges, edge)
	}
	definition, err := domain.NewWorkflowDefinitionWithExecutionProfile(
		candidate.Input.Definition.DefinitionID,
		3,
		"PostgreSQL Workflow Input Authority canary",
		"6",
		nodes,
		edges,
		workflowruntime.ProjectBriefInputContract(),
		workflowruntime.ApplicationOutputContract(),
		domain.WorkflowExecutionProfileRef{
			Version: ExecutionProfileV3, Hash: strings.TrimPrefix(executionProfileHashV3, "sha256:"),
		},
		candidate.Input.Run.StartedBy,
		now,
	)
	if err != nil {
		t.Fatalf("build exact PostgreSQL v3 definition: %v", err)
	}
	if err := workflowruntime.ValidateDefinitionForExecutionProfile(definition, workflowruntime.WorkflowExecutionProfileV3Ref()); err != nil {
		t.Fatalf("validate PostgreSQL canary production path: %v", err)
	}
	definitionRaw, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	candidate.Materials.Definition = definitionRaw
	candidate.Input.Definition.DefinitionHash = authorityHash(definition.Hash)
	candidate.Input.Definition.DefinitionVersion = int64(definition.Version)
	candidate.Input.Definition.RawBytesHash = RawSHA256(definitionRaw)
	candidate.Input.Definition.RawBytesSize = int64(len(definitionRaw))
	var bundle core.WorkbenchBundle
	if err := json.Unmarshal(candidate.Materials.BuildManifest, &bundle); err != nil {
		t.Fatalf("decode PostgreSQL canary Workbench bundle: %v", err)
	}
	if bundle.DeliverySliceID == nil {
		t.Fatal("PostgreSQL canary Workbench bundle lacks its delivery slice")
	}
	var contract constructor.ContractContent
	if err := json.Unmarshal(candidate.Materials.BuildContract, &contract); err != nil {
		t.Fatalf("decode PostgreSQL canary BuildContract: %v", err)
	}
	contract.DeliverySliceID = *bundle.DeliverySliceID
	contract.FullStackTemplate.ID = postgresStoreCanaryFullStackTemplateID
	contract.FullStackTemplate.Certification = "approved"
	contractHash, err := domain.CanonicalHash(contract)
	if err != nil {
		t.Fatalf("hash PostgreSQL canary BuildContract: %v", err)
	}
	contractRaw, err := json.Marshal(contract)
	if err != nil {
		t.Fatal(err)
	}
	candidate.Materials.BuildContract = contractRaw
	candidate.Input.Build.BuildContract.ContentHash = RawSHA256(contractRaw)
	candidate.Input.Build.BuildContract.ContractHash = authorityHash(contractHash)
	candidate.Input.Build.BuildContract.RawBytesHash = RawSHA256(contractRaw)
	candidate.Input.Build.BuildContract.RawBytesSize = int64(len(contractRaw))
	candidate.Document, err = candidateDocumentFromRecord(candidate.Input, candidate.Materials)
	if err != nil {
		t.Fatalf("rebuild PostgreSQL canary private Candidate: %v", err)
	}
	return candidate
}

type postgresStoreCanaryArtifactAuthority struct{}

func (postgresStoreCanaryArtifactAuthority) Readiness(context.Context) error { return nil }

func (postgresStoreCanaryArtifactAuthority) Verify(
	_ context.Context,
	request templatecatalog.ArtifactAuthorityVerifyRequest,
) (templatecatalog.ArtifactAuthorityReceipt, error) {
	verifiedAt := time.Now().UTC().Truncate(time.Microsecond)
	digest := func(label string) string { return RawSHA256([]byte("template-authority:" + label)) }
	referenceDigest := func(reference string) string {
		separator := strings.LastIndex(reference, "@")
		if separator < 0 {
			return ""
		}
		return reference[separator+1:]
	}
	artifactDigest := referenceDigest(request.Bundle.ArtifactReference)
	serviceImageDigest := referenceDigest(request.Bundle.ServiceSBOMs[0].ImageReference)
	serviceReferrerDigest := referenceDigest(request.Bundle.ServiceSBOMs[0].ReferrerReference)
	evidence := make([]templatecatalog.GateEvidence, 0, len(templatecatalog.RequiredAdmissionGates()))
	for _, gate := range templatecatalog.RequiredAdmissionGates() {
		evidence = append(evidence, templatecatalog.GateEvidence{
			Gate: gate, Outcome: templatecatalog.EvidencePassed, SubjectHash: request.SubjectHash,
			Digest: digest("evidence:" + string(gate)), Reference: "urn:worksflow:template-evidence:" + string(gate),
			Producer: "workflow-input-store-canary/v1", InvocationID: "store-canary-" + string(gate),
			ObservedAt: verifiedAt,
		})
	}
	entryID := "entry-" + strings.ReplaceAll(request.Candidate.Manifest.TemplateID, "-", ".")
	signatureDigest := digest("signature:" + request.Candidate.Manifest.TemplateID)
	return templatecatalog.NewArtifactAuthorityReceipt(templatecatalog.NewArtifactAuthorityReceiptInput{
		ID:                    uuid.NewSHA1(uuid.NameSpaceURL, []byte("workflow-input-store-template-authority:"+request.Candidate.Manifest.TemplateID)).String(),
		SubjectHash:           request.SubjectHash,
		SourceTreeHash:        request.Candidate.Source.TreeHash,
		ArtifactDigest:        artifactDigest,
		SBOMDigest:            request.Candidate.SBOMDigest,
		SignatureBundleDigest: signatureDigest,
		PolicyHash:            digest("policy"),
		Authority: templatecatalog.ArtifactAuthorityIdentity{
			ID: "workflow-input-store-canary-authority", Version: "1.0.0",
		},
		VerifierImageDigest: digest("verifier-image"),
		TrustRootDigest:     digest("trust-root"),
		TransparencyLog: templatecatalog.ArtifactTransparencyLog{
			ID: "rekor.store-canary", EntryUUID: entryID, LogIndex: 0, IntegratedAt: verifiedAt,
		},
		VerificationReference: request.Bundle.VerificationReference,
		ArtifactDescriptor: templatecatalog.ArtifactDescriptor{
			Reference: request.Bundle.ArtifactReference,
			MediaType: "application/vnd.oci.image.manifest.v1+json",
			Digest:    artifactDigest, SizeBytes: 100,
			Config: templatecatalog.ArtifactBlobDescriptor{
				MediaType: "application/vnd.oci.image.config.v1+json", Digest: digest("config"), SizeBytes: 20,
			},
			Layers: []templatecatalog.ArtifactBlobDescriptor{{
				MediaType: "application/vnd.oci.image.layer.v1.tar+gzip", Digest: digest("layer"), SizeBytes: 30,
			}},
			TotalBytes: 150,
		},
		SBOMDescriptor: templatecatalog.ArtifactSBOMDescriptor{
			SchemaVersion: "worksflow.template-sbom-aggregate/v1", Digest: request.Candidate.SBOMDigest,
			ServiceCount: len(request.Bundle.ServiceSBOMs),
			Services: []templatecatalog.ArtifactSBOMServiceDescriptor{{
				ServiceID:      request.Bundle.ServiceSBOMs[0].ServiceID,
				ImageReference: request.Bundle.ServiceSBOMs[0].ImageReference, ImageDigest: serviceImageDigest,
				ReferrerReference: request.Bundle.ServiceSBOMs[0].ReferrerReference, ReferrerDigest: serviceReferrerDigest,
				StatementDigest: digest("sbom-statement"), PredicateDigest: digest("sbom-predicate"),
				SPDXVersion: "SPDX-2.3", DocumentNamespace: "https://spdx.example.test/" + request.Candidate.Manifest.TemplateID,
				EvidenceHash: digest("sbom-evidence"),
			}},
		},
		Proof: templatecatalog.ArtifactAuthorityProof{
			PayloadType: "application/vnd.in-toto+json", PredicateType: "https://slsa.dev/provenance/v1",
			PayloadDigest: digest("payload"), SignatureBundleDigest: signatureDigest,
			SignerIdentities:         []string{"https://github.com/ai-worksflow/templates/.github/workflows/admit.yml@refs/heads/main"},
			TransparencyBundleDigest: digest("transparency-bundle"), LogID: "rekor.store-canary",
			EntryUUID: entryID, LogIndex: 0, TreeSize: 1, RootHash: digest("transparency-root"),
			IntegratedAt: verifiedAt, CheckpointSignedAt: verifiedAt,
		},
		Evidence: evidence,
		Signature: templatecatalog.SignatureEnvelope{
			Format: "dsse", SubjectHash: request.SubjectHash, BundleDigest: signatureDigest,
			Signer:             "https://github.com/ai-worksflow/templates/.github/workflows/admit.yml@refs/heads/main",
			TransparencyLogRef: "urn:rekor:" + entryID, SignedAt: verifiedAt,
		},
		VerifiedAt: verifiedAt, RecordedBy: request.RecordedBy, CreatedAt: verifiedAt,
	})
}

func bindPostgresStoreCanaryTemplate(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	candidate Candidate,
) Candidate {
	t.Helper()
	if _, err := database.ExecContext(ctx, `INSERT INTO users(id,email,display_name,password_hash) VALUES
  ($1,$2,'Workflow Input Store canary','x'),
  ($3,$4,'Workflow Input template evaluator','x')`,
		candidate.Input.Run.StartedBy, candidate.Input.Run.StartedBy+"@example.test",
		postgresStoreCanaryTemplateEvaluatorID, postgresStoreCanaryTemplateEvaluatorID+"@example.test"); err != nil {
		t.Fatalf("insert Workflow Input store template actors: %v", err)
	}
	gormDatabase, err := gorm.Open(postgres.New(postgres.Config{Conn: database}), &gorm.Config{})
	if err != nil {
		t.Fatalf("open Workflow Input store Template Writer database: %v", err)
	}
	writer, err := templatecatalog.NewWriter(gormDatabase, postgresStoreCanaryArtifactAuthority{})
	if err != nil {
		t.Fatal(err)
	}
	namespace := uuid.MustParse(candidate.Input.Project.ID)
	admit := func(role string) templatecatalog.TemplateRelease {
		t.Helper()
		templateID := "workflow-input-store-" + role
		admission := postgresStoreCanaryTemplateAdmission(
			uuid.NewSHA1(namespace, []byte("template-attempt:"+role)).String(),
			uuid.NewSHA1(namespace, []byte("template-release:"+role)).String(),
			templateID,
			role,
			candidate.Input.Run.StartedBy,
		)
		registration, err := writer.Admit(ctx, admission)
		if err != nil {
			t.Fatalf("admit Workflow Input store %s template: %v", role, err)
		}
		if registration.Release == nil {
			t.Fatalf("Workflow Input store %s template was not approved", role)
		}
		return registration.Release.Release
	}
	apiRelease := admit("api")
	webRelease := admit("web")
	registered, err := writer.RegisterFullStack(ctx, templatecatalog.RegisterFullStackInput{
		ID: postgresStoreCanaryFullStackTemplateID, TemplateID: "workflow-input-store-stack", Version: "1.0.0",
		Components: []templatecatalog.FullStackComponentSelection{
			{Role: "api", MountPath: "services/api", Release: templatecatalog.TemplateReleaseRef{
				ID: apiRelease.ID(), ContentHash: apiRelease.ContentHash(), SubjectHash: apiRelease.SubjectHash(),
			}},
			{Role: "web", MountPath: "apps/web", Release: templatecatalog.TemplateReleaseRef{
				ID: webRelease.ID(), ContentHash: webRelease.ContentHash(), SubjectHash: webRelease.SubjectHash(),
			}},
		},
		Layout: templatecatalog.FullStackLayout{
			ContractTruthSource: "openapi", OpenAPIPath: "contracts/openapi.yaml",
			GeneratedClientPath: "packages/api-client", DeploymentPath: "deployment",
			TestPath: "tests", DatabaseEngine: "postgresql",
		},
		CreatedBy: postgresStoreCanaryTemplateEvaluatorID, CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
	})
	if err != nil {
		t.Fatalf("register Workflow Input store FullStackTemplate: %v", err)
	}
	stack := registered.Template.Snapshot()
	var contract constructor.ContractContent
	if err := json.Unmarshal(candidate.Materials.BuildContract, &contract); err != nil {
		t.Fatalf("decode Workflow Input store BuildContract for template binding: %v", err)
	}
	contract.FullStackTemplate.ID = stack.ID
	contract.FullStackTemplate.ContentHash = stack.ContentHash
	contract.FullStackTemplate.Certification = "approved"
	contract.FullStackTemplate.PolicyStatus = "active"
	contract.TemplateReleaseRefs = []constructor.TemplateReleaseRef{
		{ID: apiRelease.ID(), ReleaseHash: apiRelease.ContentHash(), Role: "api", Certification: "approved", PolicyStatus: "active"},
		{ID: webRelease.ID(), ReleaseHash: webRelease.ContentHash(), Role: "web", Certification: "approved", PolicyStatus: "active"},
	}
	contractHash, err := domain.CanonicalHash(contract)
	if err != nil {
		t.Fatalf("hash Workflow Input store template-bound BuildContract: %v", err)
	}
	contractRaw, err := json.Marshal(contract)
	if err != nil {
		t.Fatal(err)
	}
	candidate.Materials.BuildContract = contractRaw
	candidate.Input.Build.BuildContract.ContentHash = RawSHA256(contractRaw)
	candidate.Input.Build.BuildContract.ContractHash = authorityHash(contractHash)
	candidate.Input.Build.BuildContract.RawBytesHash = RawSHA256(contractRaw)
	candidate.Input.Build.BuildContract.RawBytesSize = int64(len(contractRaw))
	candidate.Document, err = candidateDocumentFromRecord(candidate.Input, candidate.Materials)
	if err != nil {
		t.Fatalf("rebuild Workflow Input store Candidate with exact FullStackTemplate: %v", err)
	}
	return candidate
}

func postgresStoreCanaryTemplateAdmission(
	attemptID,
	releaseID,
	templateID,
	role,
	requestedBy string,
) templatecatalog.AdmitInput {
	digest := func(label string) string { return RawSHA256([]byte("template-candidate:" + templateID + ":" + label)) }
	portName := role + "-http"
	candidate := templatecatalog.AdmissionCandidate{
		Source: templatecatalog.TemplateSource{
			Repository: "https://github.com/ai-worksflow/templates.git", Branch: templateID,
			Commit: strings.Repeat("a", 40), TreeHash: digest("tree"),
		},
		Manifest: templatecatalog.TemplateManifest{
			SchemaVersion: templatecatalog.TemplateManifestSchemaVersion, TemplateID: templateID,
			DisplayName: templateID, Version: "1.0.0",
			Services: []templatecatalog.TemplateService{{ID: role, Kind: role, RootPath: "."}},
			Toolchains: []templatecatalog.Toolchain{{
				Name: "runtime", Version: "22.0.0", Image: "ghcr.io/worksflow/runtime@" + digest("runtime"),
			}},
			Commands: map[string]templatecatalog.Command{
				"install":   {WorkingDirectory: ".", Argv: []string{"pnpm", "install", "--frozen-lockfile"}},
				"lint":      {WorkingDirectory: ".", Argv: []string{"pnpm", "lint"}},
				"typecheck": {WorkingDirectory: ".", Argv: []string{"pnpm", "typecheck"}},
				"test":      {WorkingDirectory: ".", Argv: []string{"pnpm", "test"}},
				"build":     {WorkingDirectory: ".", Argv: []string{"pnpm", "build"}},
				"start":     {WorkingDirectory: ".", Argv: []string{"pnpm", "start"}},
			},
			Ports: []templatecatalog.Port{{
				Name: portName, ServiceID: role, Number: 3000, Protocol: "http", Exposure: "preview",
			}},
			HealthChecks: []templatecatalog.HealthCheck{{
				ID: role + "-health", ServiceID: role, PortName: portName, Path: "/health",
			}},
			BuildOutputs:   []templatecatalog.BuildOutput{{ServiceID: role, Path: "dist"}},
			ExtensionPaths: []string{"src"}, ProtectedPaths: []string{"templates.lock.json"},
			EnvironmentSchema: []templatecatalog.EnvironmentVariable{{
				Name: "PORT", Required: true, Description: "service port",
			}},
			Lockfiles: []templatecatalog.Lockfile{{
				Path: "pnpm-lock.yaml", Digest: digest("lockfile"), Registry: "https://registry.npmjs.org",
			}},
			ProfileDigest: digest("profile"),
		},
		SBOMDigest: digest("sbom"), LicenseExpression: "Apache-2.0", LicenseDigest: digest("license"),
	}
	return templatecatalog.AdmitInput{
		AttemptID: attemptID, ReleaseID: releaseID, Candidate: candidate,
		Bundle: templatecatalog.ArtifactAdmissionBundle{
			ArtifactReference: "ghcr.io/worksflow/templates/" + templateID + "@" + digest("artifact"),
			ServiceSBOMs: []templatecatalog.ArtifactServiceSBOMReference{{
				ServiceID:         role,
				ImageReference:    "ghcr.io/worksflow/templates/" + templateID + "-service@" + digest("service-image"),
				ReferrerReference: "ghcr.io/worksflow/templates/" + templateID + "-sbom@" + digest("sbom-referrer"),
			}},
			DSSEEnvelope:          []byte(`{"payloadType":"application/vnd.in-toto+json","payload":"canary","signatures":[]}`),
			TransparencyBundle:    []byte(`{"kind":"rekorInclusionProof","canary":true}`),
			VerificationReference: "urn:worksflow:template-verification:" + templateID,
		},
		RequestedBy: requestedBy, EvaluatedBy: postgresStoreCanaryTemplateEvaluatorID,
	}
}

func seedPostgresStoreCanary(t *testing.T, ctx context.Context, database *sql.DB, candidate Candidate) {
	t.Helper()
	input := candidate.Input
	manifest := inputManifestByRole(t, input, candidate.Materials, ManifestRoleRun)
	if RawSHA256(manifest.raw) != manifest.binding.RawBytesHash || int64(len(manifest.raw)) != manifest.binding.RawBytesSize {
		t.Fatal("PostgresStore fixture manifest bytes do not match its compiled binding")
	}
	sourceRevision, sourceRaw := revisionByPurpose(t, input, candidate.Materials, "governed-source")
	workspaceRevision, workspaceRaw := revisionByPurpose(t, input, candidate.Materials, RevisionPurposeWorkspaceTarget)
	receiptBinding := input.ReviewReceipts[0]
	receiptRaw := candidate.Materials.ReviewReceipts[0].Bytes
	receipt, err := canonicalreviewreceipt.Decode(receiptRaw, receiptBinding.ReceiptHash)
	if err != nil {
		t.Fatalf("decode Canonical Review fixture: %v", err)
	}
	var bundle core.WorkbenchBundle
	if err := json.Unmarshal(candidate.Materials.BuildManifest, &bundle); err != nil {
		t.Fatalf("decode Workbench bundle fixture: %v", err)
	}
	var contract constructor.ContractContent
	if err := json.Unmarshal(candidate.Materials.BuildContract, &contract); err != nil {
		t.Fatalf("decode BuildContract fixture: %v", err)
	}
	if len(contract.TemplateReleaseRefs) != 2 {
		t.Fatalf("BuildContract Template Release count = %d, want exact api/web pair", len(contract.TemplateReleaseRefs))
	}
	var envelope domain.NodeInputEnvelope
	if err := json.Unmarshal(candidate.Materials.NodeInput, &envelope); err != nil {
		t.Fatalf("decode quality NodeInput fixture: %v", err)
	}
	bindings := envelope.Bindings()
	if len(bindings) != 1 {
		t.Fatalf("quality NodeInput binding count = %d, want 1", len(bindings))
	}
	var qualityOutput struct {
		Findings struct {
			ReportArtifactID string `json:"reportArtifactId"`
			ReportRevisionID string `json:"reportRevisionId"`
			Score            int    `json:"score"`
		} `json:"findings"`
	}
	if err := json.Unmarshal(bindings[0].Value, &qualityOutput); err != nil {
		t.Fatalf("decode typed quality result fixture: %v", err)
	}

	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	fail := func(label string, err error) {
		_ = transaction.Rollback()
		t.Fatalf("%s: %v", label, err)
	}
	contextDocument := map[string]any{
		"nodes": map[string]any{ExternalQualificationGate: map[string]any{"input": json.RawMessage(candidate.Materials.NodeInput)}},
	}
	contextRaw, err := json.Marshal(contextDocument)
	if err != nil {
		fail("encode run context", err)
	}
	statements := []struct {
		label string
		query string
		args  []any
	}{
		{
			"insert project", `INSERT INTO projects(id,name,created_by,governance_mode) VALUES($1,'Workflow Input Store canary',$2,'solo')`,
			[]any{input.Project.ID, input.Run.StartedBy},
		},
		{
			"insert owner membership", `INSERT INTO project_members(project_id,user_id,role) VALUES($1,$2,'owner')`,
			[]any{input.Project.ID, input.Run.StartedBy},
		},
		{
			"insert input manifest", `INSERT INTO input_manifests(
			  id,project_id,kind,schema_version,content_store,content_ref,content_hash,manifest_hash,created_by,created_at
			) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
			[]any{manifest.binding.ID, input.Project.ID, manifest.binding.Kind, manifest.binding.SchemaVersion,
				manifest.binding.ContentStore, manifest.binding.ContentRef, manifest.binding.ContentHash,
				manifest.binding.ManifestHash, input.Run.StartedBy, time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)},
		},
		{
			"insert source artifact", `INSERT INTO artifacts(
			  id,project_id,kind,artifact_key,title,lifecycle,version,created_by
			) VALUES($1,$2,$3,$4,'Governed source','active',2,$5)`,
			[]any{sourceRevision.ArtifactID, input.Project.ID, sourceRevision.ArtifactKind,
				"source-" + sourceRevision.ArtifactID, input.Run.StartedBy},
		},
		{
			"insert workspace artifact", `INSERT INTO artifacts(
			  id,project_id,kind,artifact_key,title,lifecycle,version,created_by
			) VALUES($1,$2,'workspace',$3,'Workspace','active',1,$4)`,
			[]any{workspaceRevision.ArtifactID, input.Project.ID, "workspace-" + workspaceRevision.ArtifactID, input.Run.StartedBy},
		},
		{
			"insert report artifact", `INSERT INTO artifacts(
			  id,project_id,kind,artifact_key,title,lifecycle,version,created_by
			) VALUES($1,$2,'quality_report',$3,'Quality report','active',1,$4)`,
			[]any{qualityOutput.Findings.ReportArtifactID, input.Project.ID,
				"quality-" + qualityOutput.Findings.ReportArtifactID, input.Run.StartedBy},
		},
		{
			"insert source revision", `INSERT INTO artifact_revisions(
			  id,artifact_id,revision_number,schema_version,content_store,content_ref,content_hash,byte_size,
			  workflow_status,change_source,change_summary,source_manifest_id,created_by,created_at,approved_at
			) VALUES($1,$2,1,$3,$4,$5,$6,$7,'approved','human','Reviewed source',$8,$9,$10,$11)`,
			[]any{sourceRevision.RevisionID, sourceRevision.ArtifactID, sourceRevision.SchemaVersion,
				sourceRevision.ContentStore, sourceRevision.ContentRef, sourceRevision.ContentHash, len(sourceRaw),
				manifest.binding.ID, input.Run.StartedBy,
				time.Date(2026, 7, 18, 9, 0, 0, 0, time.UTC), time.Date(2026, 7, 18, 12, 0, 0, 0, time.UTC)},
		},
		{
			"insert report revision", `INSERT INTO artifact_revisions(
			  id,artifact_id,revision_number,schema_version,content_store,content_ref,content_hash,byte_size,
			  workflow_status,change_source,change_summary,created_by,created_at,approved_at
			) VALUES($1,$2,1,1,'repository',$3,$4,2,'approved','system','Quality report',$5,$6,$6)`,
			[]any{qualityOutput.Findings.ReportRevisionID, qualityOutput.Findings.ReportArtifactID,
				"objects/revisions/report", RawSHA256([]byte(`{}`)), input.Run.StartedBy,
				time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)},
		},
		{
			"insert definition", `INSERT INTO workflow_definitions(id,project_id,workflow_key,title,created_by)
			VALUES($1,$2,'workflow-input-store-canary','Workflow Input Store canary',$3)`,
			[]any{input.Definition.DefinitionID, input.Project.ID, input.Run.StartedBy},
		},
		{
			"insert definition version", `INSERT INTO workflow_definition_versions(
			  id,definition_id,version,schema_version,content,content_hash,validation_report,published,created_by,
			  execution_profile_version,execution_profile_hash
			) VALUES($1,$2,$3,1,$4,$5,'{}',true,$6,$7,$8)`,
			[]any{input.Definition.DefinitionVersionID, input.Definition.DefinitionID, input.Definition.DefinitionVersion,
				string(candidate.Materials.Definition), strings.TrimPrefix(input.Definition.DefinitionHash, "sha256:"),
				input.Run.StartedBy, input.Definition.ExecutionProfileVersion,
				strings.TrimPrefix(input.Definition.ExecutionProfileHash, "sha256:")},
		},
		{
			"insert workflow run", `INSERT INTO workflow_runs(
			  id,project_id,definition_version_id,status,input_manifest_id,scope,context,event_cursor,
			  started_by,started_at,execution_profile_version,execution_profile_hash,governance_mode
			) VALUES($1,$2,$3,'running',$4,$5,$6,$7,$8,$9,$10,$11,'solo')`,
			[]any{input.Run.ID, input.Project.ID, input.Definition.DefinitionVersionID, input.Run.InputManifestID,
				string(candidate.Materials.RunScope), string(contextRaw), candidate.Request.ExpectedRunCursor,
				input.Run.StartedBy, time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC),
				input.Definition.ExecutionProfileVersion, strings.TrimPrefix(input.Definition.ExecutionProfileHash, "sha256:")},
		},
		{
			"insert external gate node", `INSERT INTO workflow_node_runs(
			  id,run_id,node_key,node_type,status,definition_node_id,slice_kind
			) VALUES($1,$2,$3,$4,'pending',$5,'root')`,
			[]any{candidate.Request.NodeRunID, input.Run.ID, candidate.Request.NodeKey, input.Gate.NodeType,
				input.Gate.DefinitionNodeID},
		},
		{
			"insert build manifest", `INSERT INTO application_build_manifests(
			  id,project_id,workflow_run_id,schema_version,content_store,content_ref,content_hash,manifest_hash,status,
			  created_by,root_manifest_id,workspace_revision_id,root_ordinal,manifest_group_key,delivery_slice_id
			) VALUES($1,$2,$3,1,'repository',$4,$5,$6,'frozen',$7,$1,NULL,0,$8,$9)`,
			[]any{input.Build.BuildManifest.ID, input.Project.ID, input.Run.ID,
				"objects/build-manifests/" + input.Build.BuildManifest.ID, input.Build.BuildManifest.ContentHash,
				input.Build.BuildManifest.ManifestHash, input.Run.StartedBy, *bundle.ManifestGroupKey, *bundle.DeliverySliceID},
		},
		{
			"insert build contract", `INSERT INTO application_build_contracts(
			  id,project_id,build_manifest_id,build_manifest_hash,full_stack_template_id,full_stack_template_hash,
			  schema_version,compiler_version,compiler_hash,content_store,content_ref,content_hash,contract_hash,status,
			  must_count,must_ready_count,obligation_count,source_count,template_release_count,blocking_count,conflict_count,created_by
			) VALUES($1,$2,$3,$4,$5,$6,'application-build-contract/v2','worksflow-constraint-compiler/v7',$7,
			  'repository',$8,$9,$10,'ready',1,1,1,1,0,0,0,$11)`,
			[]any{input.Build.BuildContract.ID, input.Project.ID, input.Build.BuildManifest.ID,
				input.Build.BuildManifest.ManifestHash, postgresStoreCanaryFullStackTemplateID, contract.FullStackTemplate.ContentHash,
				strings.Repeat("9", 64), "objects/build-contracts/" + input.Build.BuildContract.ID,
				input.Build.BuildContract.ContentHash, input.Build.BuildContract.ContractHash, input.Run.StartedBy},
		},
		{
			"insert build contract source", `INSERT INTO application_build_contract_sources(
			  contract_id,ordinal,source_kind,purpose,required,artifact_id,revision_id,content_hash
			) VALUES($1,0,$2,$3,true,$4,$5,$6)`,
			[]any{input.Build.BuildContract.ID, sourceRevision.ArtifactKind, sourceRevision.Purpose,
				sourceRevision.ArtifactID, sourceRevision.RevisionID, sourceRevision.ContentHash},
		},
		{
			"insert build contract template releases", `INSERT INTO application_build_contract_template_releases(
			  contract_id,ordinal,role,template_release_id,template_release_content_hash
			) VALUES($1,0,$2,$3,$4),($1,1,$5,$6,$7)`,
			[]any{input.Build.BuildContract.ID,
				contract.TemplateReleaseRefs[0].Role, contract.TemplateReleaseRefs[0].ID, contract.TemplateReleaseRefs[0].ReleaseHash,
				contract.TemplateReleaseRefs[1].Role, contract.TemplateReleaseRefs[1].ID, contract.TemplateReleaseRefs[1].ReleaseHash},
		},
		{
			"insert build contract obligation", `INSERT INTO application_build_contract_obligations(
			  contract_id,obligation_id,level,kind,source_artifact_id,source_revision_id,source_content_hash,
			  source_anchor_id,oracle_ids,depends_on,waivable,status
			) VALUES($1,'obligation','must','source',$2,$3,$4,'root','["oracle"]','[]',false,'ready')`,
			[]any{input.Build.BuildContract.ID, sourceRevision.ArtifactID, sourceRevision.RevisionID, sourceRevision.ContentHash},
		},
		{
			"insert implementation proposal", `INSERT INTO implementation_proposals(
			  id,project_id,build_manifest_id,status,version,content_store,content_ref,content_hash,payload_hash,
			  operation_count,accepted_count,rejected_count,created_by,applied_by,applied_at,
			  application_build_contract_id,application_build_contract_hash,unimplemented_count,blocking_diagnostic_count
			) VALUES($1,$2,$3,'applied',1,'repository',$4,$5,$5,0,0,0,$6,$6,$7,$8,$9,0,0)`,
			[]any{*workspaceRevision.ImplementationProposalID, input.Project.ID, input.Build.BuildManifest.ID,
				"objects/implementation/" + *workspaceRevision.ImplementationProposalID, workspaceRevision.ContentHash,
				input.Run.StartedBy, time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC),
				input.Build.BuildContract.ID, input.Build.BuildContract.ContractHash},
		},
		{
			"insert workspace revision", `INSERT INTO artifact_revisions(
			  id,artifact_id,revision_number,schema_version,content_store,content_ref,content_hash,byte_size,
			  workflow_status,change_source,change_summary,source_manifest_id,created_by,created_at,approved_at,implementation_proposal_id
			) VALUES($1,$2,1,$3,$4,$5,$6,$7,'approved','system','Built workspace',$8,$9,$10,$10,$11)`,
			[]any{workspaceRevision.RevisionID, workspaceRevision.ArtifactID, workspaceRevision.SchemaVersion,
				workspaceRevision.ContentStore, workspaceRevision.ContentRef, workspaceRevision.ContentHash, len(workspaceRaw),
				manifest.binding.ID, input.Run.StartedBy, time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC),
				*workspaceRevision.ImplementationProposalID},
		},
		{
			"bind build manifest workspace", `UPDATE application_build_manifests
			SET workspace_revision_id=$2 WHERE id=$1`,
			[]any{input.Build.BuildManifest.ID, workspaceRevision.RevisionID},
		},
		{
			"set artifact heads", `UPDATE artifacts AS artifact SET
			  latest_revision_id=head.revision_id, latest_approved_revision_id=head.revision_id
			FROM (VALUES ($1::uuid,$2::uuid),($3::uuid,$4::uuid),($5::uuid,$6::uuid)) AS head(artifact_id,revision_id)
			WHERE artifact.id=head.artifact_id`,
			[]any{sourceRevision.ArtifactID, sourceRevision.RevisionID, workspaceRevision.ArtifactID,
				workspaceRevision.RevisionID, qualityOutput.Findings.ReportArtifactID, qualityOutput.Findings.ReportRevisionID},
		},
		{
			"insert source node", `INSERT INTO workflow_node_runs(
			  id,run_id,node_key,node_type,status,input_manifest_id,output_revision_id,definition_node_id,slice_kind
			) VALUES($1,$2,$3,$4,'completed',$5,$6,$7,'root')`,
			[]any{input.Predecessors[0].SourceNodeRunID, input.Run.ID, input.Predecessors[0].SourceNodeKey,
				input.Predecessors[0].SourceNodeType, manifest.binding.ID, workspaceRevision.RevisionID,
				input.Predecessors[0].SourceDefinitionNodeID},
		},
		{
			"consume build manifest", `UPDATE application_build_manifests SET status='consumed' WHERE id=$1`,
			[]any{input.Build.BuildManifest.ID},
		},
		{
			"insert quality run", `INSERT INTO quality_runs(
			  id,project_id,workflow_run_id,workspace_artifact_id,workspace_revision_id,workspace_content_hash,
			  report_artifact_id,report_revision_id,status,score,runner_version,sandbox_kind,created_by,started_at,completed_at
			) VALUES($1,$2,$3,$4,$5,$6,$7,$8,'passed',$9,'store-canary','container',$10,$11,$11)`,
			[]any{input.QualityResult.QualityRunID, input.Project.ID, input.Run.ID, workspaceRevision.ArtifactID,
				workspaceRevision.RevisionID, workspaceRevision.ContentHash, qualityOutput.Findings.ReportArtifactID,
				qualityOutput.Findings.ReportRevisionID, qualityOutput.Findings.Score, input.Run.StartedBy,
				time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)},
		},
	}
	for _, statement := range statements {
		if _, err := transaction.ExecContext(ctx, statement.query, statement.args...); err != nil {
			fail(statement.label, err)
		}
	}
	if err := transaction.Commit(); err != nil {
		t.Fatalf("commit PostgresStore fixture: %v", err)
	}

	reviewTransaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	reviewFail := func(label string, err error) {
		_ = reviewTransaction.Rollback()
		t.Fatalf("%s: %v", label, err)
	}
	policy, err := json.Marshal(receipt.Policy.Value)
	if err != nil {
		reviewFail("encode Canonical Review policy", err)
	}
	if _, err := reviewTransaction.ExecContext(ctx, `INSERT INTO review_requests(
		id,project_id,artifact_id,revision_id,content_hash,status,policy,requested_by,requested_at,review_authority_version
	) VALUES($1,$2,$3,$4,$5,'open',$6,$7,$8,1)`,
		receipt.ReviewRequest.ID, receipt.ReviewRequest.ProjectID, receipt.ReviewRequest.ArtifactID,
		receipt.ReviewRequest.RevisionID, receipt.ReviewRequest.ContentHash, string(policy),
		receipt.ReviewRequest.RequestedBy, mustPostgresStoreCanaryTime(t, receipt.ReviewRequest.RequestedAt)); err != nil {
		reviewFail("insert Canonical Review request", err)
	}
	decision := receipt.Decisions.Decisions[0]
	if _, err := reviewTransaction.ExecContext(ctx, `INSERT INTO review_decisions(
		id,review_request_id,reviewer_id,decision,summary,created_at,solo_self_review,
		review_authority_version,reviewer_role_at_decision,governance_mode_at_decision,
		owner_count_at_decision,sole_owner_id_at_decision,solo_review_confirmed,precondition_etag
	) VALUES($1,$2,$3,$4,$5,$6,$7,1,$8,$9,$10,$11,$12,$13)`,
		decision.ID, receipt.ReviewRequest.ID, decision.ReviewerID, decision.Decision, decision.Summary,
		mustPostgresStoreCanaryTime(t, decision.CreatedAt), decision.SoloSelfReview,
		decision.AuthorityFacts.ReviewerRole, decision.AuthorityFacts.GovernanceMode,
		decision.AuthorityFacts.OwnerCount, decision.AuthorityFacts.SoleOwnerID,
		decision.AuthorityFacts.ExplicitConfirmation, decision.AuthorityFacts.PreconditionETag); err != nil {
		reviewFail("insert Canonical Review decision", err)
	}
	if _, err := reviewTransaction.ExecContext(ctx, `UPDATE review_requests
		SET status='approved',closed_at=$2,closed_by_decision_id=$3 WHERE id=$1`,
		receipt.ReviewRequest.ID, mustPostgresStoreCanaryTime(t, receipt.ReviewRequest.ClosedAt),
		receipt.ReviewRequest.ClosedByDecisionID); err != nil {
		reviewFail("close Canonical Review request", err)
	}
	var issuedHash string
	if err := reviewTransaction.QueryRowContext(ctx, `SELECT (issued.receipt_record).receipt_hash
		FROM issue_canonical_review_approval_receipt($1) AS issued`, receipt.ReviewRequest.ID).Scan(&issuedHash); err != nil {
		reviewFail("issue Canonical Review receipt", err)
	}
	if issuedHash != receiptBinding.ReceiptHash {
		reviewFail("compare Canonical Review receipt", &postgresStoreCanaryMismatch{got: issuedHash, want: receiptBinding.ReceiptHash})
	}
	if err := reviewTransaction.Commit(); err != nil {
		t.Fatalf("commit Canonical Review receipt fixture: %v", err)
	}
}

type postgresStoreCanaryPolicySource struct {
	resolved qualificationpolicy.ResolvedPolicy
}

func (source postgresStoreCanaryPolicySource) Resolve(
	_ context.Context,
	sourceID string,
) (qualificationpolicy.ResolvedPolicy, error) {
	if sourceID != "workflow-input-store-canary-policy" {
		return qualificationpolicy.ResolvedPolicy{}, qualificationpolicy.ErrNotFound
	}
	return source.resolved, nil
}

func bindPostgresStoreCanaryPolicy(
	t *testing.T,
	ctx context.Context,
	database *sql.DB,
	candidate Candidate,
) Candidate {
	t.Helper()
	projectID := uuid.MustParse(candidate.Input.Project.ID)
	digest := func(label string) string { return RawSHA256([]byte("qualification-policy:" + label)) }
	resolved := qualificationpolicy.ResolvedPolicy{
		ProjectID: projectID,
		ExecutionProfile: qualificationpolicy.ExecutionProfileBinding{
			Version: candidate.Input.Definition.ExecutionProfileVersion,
			Hash:    candidate.Input.Definition.ExecutionProfileHash,
		},
		ExternalGatePolicy: qualificationpolicy.ExternalGatePolicyV1,
		Status:             qualificationpolicy.AuthorityStatusActive,
		SupersessionPolicy: qualificationpolicy.SupersessionPolicyV1,
		RevisionPolicy: qualificationpolicy.RevisionPolicy{
			SchemaVersion:        qualificationpolicy.RevisionPolicySchemaV1,
			SourceCurrencyPolicy: qualificationpolicy.CurrencyLatestApproved,
			WorkspaceTarget: qualificationpolicy.WorkspaceTargetPolicy{
				CurrencyPolicy: qualificationpolicy.CurrencyLatestApproved,
			},
			ReviewByChangeSource: []qualificationpolicy.ChangeSourceReviewRule{
				{ChangeSource: qualificationpolicy.ChangeSourceAIProposal, CanonicalReviewRequired: true},
				{ChangeSource: qualificationpolicy.ChangeSourceHuman, CanonicalReviewRequired: true},
				{ChangeSource: qualificationpolicy.ChangeSourceImport, CanonicalReviewRequired: true},
				{ChangeSource: qualificationpolicy.ChangeSourceMerge, CanonicalReviewRequired: true},
				{ChangeSource: qualificationpolicy.ChangeSourceRollback, CanonicalReviewRequired: true},
				{ChangeSource: qualificationpolicy.ChangeSourceSystem, CanonicalReviewRequired: false},
			},
			ExactApprovedSources: []qualificationpolicy.ExactApprovedSource{},
		},
		PlanInputProfile: qualificationpolicy.PlanInputProfile{
			SchemaVersion: qualificationpolicy.PlanInputProfileSchemaV1,
			ArtifactPolicy: qualificationpolicy.ArtifactPolicy{
				MaximumArtifacts:            qualificationevidence.MaximumArtifacts,
				RequireRestrictedEncryption: true,
				RequireTrace:                true,
				RequireVideo:                true,
			},
			Artifacts: []qualificationpolicy.ArtifactExpectation{
				{ID: "browser-video", Kind: qualificationevidence.ArtifactKindVideo, Classification: qualificationevidence.ClassificationRestricted},
				{ID: "credential-safe-trace", Kind: qualificationevidence.ArtifactKindTrace, Classification: qualificationevidence.ClassificationRestricted},
				{ID: "run-result", Kind: qualificationevidence.ArtifactKindRunResult, Classification: qualificationevidence.ClassificationDistributable},
			},
			CredentialProfile: qualificationpolicy.CredentialProfile{
				Audience: "urn:worksflow:qualification", AuthorityID: "credential-authority",
				IssuanceArtifactID: "credential-set-issuance", RevocationArtifactID: "credential-set-revocation",
				MemberRequestSetDigest: digest("credential-member-request-set"),
			},
			GoldenRuntime: qualificationreceipt.GoldenRuntimeBinding{
				AuthorityDocumentArtifactID: "golden-authority-document",
				AuthorityDocumentDigest:     digest("golden-authority-document"),
				FaultOperationSetDigest:     qualificationreceipt.GoldenFaultOperationSetDigestV1,
				FixtureDocumentArtifactID:   "golden-fixture-document",
				FixtureDocumentDigest:       digest("golden-fixture-document"),
				FixtureID:                   "40000000-0000-4000-8000-000000000001",
			},
			OutputPolicy: qualificationpolicy.OutputPolicy{
				CredentialRevocation: qualificationpolicy.CredentialRevocationPolicyV1,
				PlaintextDisposition: qualificationpolicy.PlaintextDispositionPolicyV1,
				SnapshotMode:         qualificationevidence.ImmutableSnapshotMode,
			},
			Outputs: qualificationevidence.OutputExpectation{
				KMSAttestationArtifactID: "kms-encryption-attestation",
				ArtifactIndexID:          "qualification-artifact-index", ReceiptID: "qualification-receipt",
				SnapshotID: "qualification-snapshot",
			},
			QualificationManifest: qualificationpolicy.QualificationManifestBinding{
				ArtifactID: "qualification-manifest", RevisionID: "40000000-0000-4000-8000-000000000002",
				ContentHash: digest("qualification-manifest"), PlanDigest: digest("qualification-plan"),
			},
			Recipient: qualificationevidence.EncryptionRecipient{
				KeyResourceID: "qualification-kms-key", KeyVersion: "version-1",
			},
			SourcePolicyDigest: digest("source-policy"),
			TemplateRelease: qualificationreceipt.TemplateReleaseBinding{
				ID: "40000000-0000-4000-8000-000000000003", ContentHash: digest("template-release"),
				ApprovalReceiptDigest: digest("template-release-approval"),
			},
			TrustBindings: qualificationevidence.TrustBindings{
				CaptureAuthorityID: "capture-authority", CredentialAuthorityID: "credential-authority",
				EncryptionAuthorityID: "encryption-authority", IndexerAuthorityID: "indexer-authority",
				KMSAuthorityID: "kms-authority", ReceiptAuthorityID: "receipt-authority",
				SealerAuthorityID: "sealer-authority", VerifierAuthorityID: "verifier-authority",
			},
			TrustPolicyDigest: digest("trust-policy"),
		},
		PromotionPolicy: qualificationpolicy.PromotionPolicy{
			SchemaVersion:           qualificationpolicy.PromotionPolicySchemaV1,
			PlanAuthoritySchema:     qualificationpolicy.QualificationPlanAuthoritySchemaV1,
			ReceiptSchema:           qualificationpolicy.QualificationReceiptSchemaV3,
			SingleUseProtocol:       qualificationpolicy.QualificationPromotionProtocolV2,
			IndependentRequirements: []qualificationpolicy.IndependentAuthorityBinding{},
		},
	}
	store := qualificationpolicy.NewMemoryStore()
	service, err := qualificationpolicy.NewService(
		postgresStoreCanaryPolicySource{resolved: resolved},
		store,
		qualificationpolicy.DatabaseClockFunc(func(context.Context) (time.Time, error) {
			return time.Date(2026, 7, 19, 3, 0, 0, 123_000_000, time.UTC), nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	record, err := service.Issue(ctx, qualificationpolicy.IssueCommand{
		OperationID: uuid.New(), AuthorityID: uuid.New(), PolicySourceID: "workflow-input-store-canary-policy",
	})
	if err != nil {
		t.Fatalf("compile Workflow Input store policy: %v", err)
	}
	arguments := []any{
		record.Command.OperationID, record.Command.AuthorityID, record.Command.PolicySourceID,
		record.Command.ExpectedPreviousAuthorityHash, projectID, record.Document.ExecutionProfile.Version,
		record.Document.ExecutionProfile.Hash, record.Document.Generation, record.Document.Status, record.IssuedAt,
		record.Document.ExternalGatePolicy, record.Document.SupersessionPolicy,
		record.RevisionPolicyHash, record.RevisionPolicyBytes, record.RevisionPolicyBytes,
		record.PlanInputProfileHash, record.PlanInputProfileBytes, record.PlanInputProfileBytes,
		record.PromotionPolicyHash, record.PromotionPolicyBytes, record.PromotionPolicyBytes,
		record.AuthorityHash, record.DocumentBytes, record.DocumentBytes,
	}
	var storedID uuid.UUID
	var storedHash string
	if err := database.QueryRowContext(ctx, `
SELECT authority_id,authority_hash
FROM issue_qualification_policy_authority_v1(
  $1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,
  $13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24
)`, arguments...).Scan(&storedID, &storedHash); err != nil {
		t.Fatalf("issue Workflow Input store policy: %v", err)
	}
	if storedID != record.Command.AuthorityID || storedHash != record.AuthorityHash {
		t.Fatal("Workflow Input store policy projection drifted")
	}
	candidate.Input.QualificationPolicy = QualificationPolicyBinding{
		AuthorityID: record.Command.AuthorityID.String(), AuthorityHash: record.AuthorityHash,
		ExternalGatePolicy: ExternalQualificationPolicyV1,
	}
	candidate.Document, err = candidateDocumentFromRecord(candidate.Input, candidate.Materials)
	if err != nil {
		t.Fatalf("rebuild Workflow Input store Candidate with current policy: %v", err)
	}
	return candidate
}

type postgresStoreCanaryManifest struct {
	binding InputManifestBinding
	raw     []byte
}

func inputManifestByRole(
	t *testing.T,
	input WorkflowInputDocument,
	materials RetainedMaterials,
	role string,
) postgresStoreCanaryManifest {
	t.Helper()
	for _, binding := range input.InputManifests {
		if binding.Role != role {
			continue
		}
		for _, material := range materials.InputManifests {
			if material.Role == role && material.ManifestID == binding.ID {
				return postgresStoreCanaryManifest{binding: binding, raw: material.Bytes}
			}
		}
	}
	t.Fatalf("PostgresStore fixture has no %q manifest", role)
	return postgresStoreCanaryManifest{}
}

func revisionByPurpose(
	t *testing.T,
	input WorkflowInputDocument,
	materials RetainedMaterials,
	purpose string,
) (RevisionBinding, []byte) {
	t.Helper()
	for _, binding := range input.Revisions {
		if binding.Purpose != purpose {
			continue
		}
		for _, material := range materials.Revisions {
			if material.Purpose == purpose && material.RevisionID == binding.RevisionID {
				return binding, material.Bytes
			}
		}
	}
	t.Fatalf("PostgresStore fixture has no %q revision", purpose)
	return RevisionBinding{}, nil
}

func activatePostgresStoreCanary(ctx context.Context, transaction *sql.Tx, record Record) error {
	if _, err := transaction.ExecContext(ctx, `UPDATE workflow_node_runs
		SET status='waiting_qualification',input_authority_id=$2 WHERE id=$1`, record.NodeRunID, record.AuthorityID); err != nil {
		return err
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO workflow_run_events(
		id,run_id,sequence,event_type,node_key,payload
	) VALUES($1,$2,$3,'external_qualification_activated',$4,$5)`,
		record.Input.Gate.ActivationEventID, record.WorkflowRunID, record.Input.Gate.ActivationEventSequence,
		record.Input.Gate.NodeKey,
		`{"inputAuthorityId":"`+record.AuthorityID.String()+`","nodeRunId":"`+record.NodeRunID.String()+`"}`); err != nil {
		return err
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO outbox_events(
		id,aggregate_type,aggregate_id,event_type,subject,payload,headers,
		attempts,available_at,published_at,last_error,created_at
	)
	SELECT event.id,'workflow_run',event.run_id::text,event.event_type,
	       'worksflow.workflow.run.event',
	       jsonb_build_object(
	         'id',event.id::text,
	         'projectId',$2::text,
	         'runId',event.run_id::text,
	         'sequence',event.sequence,
	         'type',event.event_type,
	         'occurredAt',to_char(event.created_at AT TIME ZONE 'UTC','YYYY-MM-DD"T"HH24:MI:SS.US"Z"'),
	         'payload',event.payload,
	         'nodeKey',event.node_key
	       ),
	       '{}'::jsonb,0,event.created_at,NULL,NULL,event.created_at
	FROM workflow_run_events AS event
	WHERE event.id=$1`, record.Input.Gate.ActivationEventID, record.Input.Project.ID); err != nil {
		return err
	}
	if _, err := transaction.ExecContext(ctx, `UPDATE workflow_runs
		SET status='waiting_qualification',event_cursor=$2 WHERE id=$1`,
		record.WorkflowRunID, record.Input.Gate.ActivationEventSequence); err != nil {
		return err
	}
	_, err := transaction.ExecContext(ctx, `SET CONSTRAINTS ALL IMMEDIATE`)
	return err
}

func postgresStoreCanaryDSN(t *testing.T, dsn, schema string) string {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err == nil && parsed.Scheme != "" {
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	return strings.TrimSpace(dsn) + " search_path=" + schema
}

func mustPostgresStoreCanaryTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse("2006-01-02T15:04:05.000000Z", value)
	if err != nil {
		t.Fatalf("parse PostgreSQL canary timestamp %q: %v", value, err)
	}
	return parsed
}

type postgresStoreCanaryMismatch struct {
	got  string
	want string
}

func (mismatch *postgresStoreCanaryMismatch) Error() string {
	return "got " + mismatch.got + ", want " + mismatch.want
}
