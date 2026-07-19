package templates

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	platformmigrations "github.com/worksflow/builder/backend/migrations"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestWriterRequiresArtifactAuthorityAndKeepsTrustedResultsOutOfAdmitInput(t *testing.T) {
	authority := &fakeArtifactAuthority{}
	if _, err := NewWriter(nil, authority); !errors.Is(err, ErrRegistryUnavailable) {
		t.Fatalf("nil database did not fail closed: %v", err)
	}
	if _, err := NewWriter(&gorm.DB{}, nil); !errors.Is(err, ErrRegistryUnavailable) {
		t.Fatalf("nil Artifact Authority did not fail closed: %v", err)
	}

	inputType := reflect.TypeOf(AdmitInput{})
	for _, forbidden := range []string{
		"Evidence", "Signature", "PolicyHash", "TrustRootDigest", "VerifierImageDigest",
		"CandidateAt", "ValidationAt", "EvaluatedAt", "VerifiedAt", "Decision",
	} {
		if _, exists := inputType.FieldByName(forbidden); exists {
			t.Fatalf("caller can inject authority-owned field %s through AdmitInput", forbidden)
		}
	}
}

func TestWriterFailsClosedBeforePersistenceWhenAuthorityIsUnavailableOrMismatched(t *testing.T) {
	input := writerAdmissionInput(uuid.NewString(), uuid.NewString(), validCandidate("authority-bound-template", "api"))

	t.Run("readiness", func(t *testing.T) {
		authority := &fakeArtifactAuthority{readinessErr: errors.New("trust root is unavailable")}
		writer, err := NewWriter(&gorm.DB{}, authority)
		if err != nil {
			t.Fatal(err)
		}
		configureDeterministicWriterClock(writer)
		if _, err := writer.Admit(context.Background(), input); !errors.Is(err, ErrRegistryUnavailable) {
			t.Fatalf("unready authority did not fail closed: %v", err)
		}
		if authority.verifyCalls != 0 {
			t.Fatalf("Verify ran %d times despite failed readiness", authority.verifyCalls)
		}
	})

	t.Run("exact lineage", func(t *testing.T) {
		authority := &fakeArtifactAuthority{mutateReceipt: func(receipt *NewArtifactAuthorityReceiptInput) {
			receipt.SourceTreeHash = digest("9")
		}}
		writer, err := NewWriter(&gorm.DB{}, authority)
		if err != nil {
			t.Fatal(err)
		}
		configureDeterministicWriterClock(writer)
		if _, err := writer.Admit(context.Background(), input); !errors.Is(err, ErrAdmissionRejected) {
			t.Fatalf("wrong-tree authority receipt did not fail closed: %v", err)
		}
	})

	t.Run("independent review", func(t *testing.T) {
		writer, err := NewWriter(&gorm.DB{}, &fakeArtifactAuthority{})
		if err != nil {
			t.Fatal(err)
		}
		configureDeterministicWriterClock(writer)
		selfReviewed := input
		selfReviewed.EvaluatedBy = selfReviewed.RequestedBy
		if _, err := writer.Admit(context.Background(), selfReviewed); !errors.Is(err, ErrInvalidTemplate) {
			t.Fatalf("self-review was not rejected before persistence: %v", err)
		}
		selfReviewed.RequestedBy = "AAAAAAAA-AAAA-4AAA-8AAA-AAAAAAAAAAAA"
		selfReviewed.EvaluatedBy = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
		if _, err := writer.Admit(context.Background(), selfReviewed); !errors.Is(err, ErrInvalidTemplate) {
			t.Fatalf("textually different forms of the same reviewer were accepted: %v", err)
		}
	})
}

func TestWriterPostgresPersistsExactAuthorityLineageAndFullStackTransactions(t *testing.T) {
	ctx, gormDB, database := setupWriterPostgres(t)
	authority := &fakeArtifactAuthority{}
	writer, err := NewWriter(gormDB, authority)
	if err != nil {
		t.Fatal(err)
	}
	configureDeterministicWriterClock(writer)

	t.Run("authority verification failure writes nothing", func(t *testing.T) {
		failingWriter, err := NewWriter(gormDB, &fakeArtifactAuthority{verifyErr: errors.New("invalid DSSE signature")})
		if err != nil {
			t.Fatal(err)
		}
		configureDeterministicWriterClock(failingWriter)
		input := writerAdmissionInput(uuid.NewString(), uuid.NewString(), validCandidate("writer-rejected-template", "api"))
		if _, err := failingWriter.Admit(ctx, input); err == nil {
			t.Fatal("Artifact Authority verification error was accepted")
		}
		if got := writerRowCount(t, ctx, database, "template_admission_attempts", "id = $1", input.AttemptID); got != 0 {
			t.Fatalf("failed verification retained %d admission rows", got)
		}
	})

	apiInput := writerAdmissionInput(uuid.NewString(), uuid.NewString(), validCandidate("writer-api-template", "api"))
	apiRegistration, err := writer.Admit(ctx, apiInput)
	if err != nil {
		t.Fatalf("approve API template: %v", err)
	}
	assertApprovedAuthorityRegistration(t, apiRegistration)

	webInput := writerAdmissionInput(uuid.NewString(), uuid.NewString(), validCandidate("writer-web-template", "web"))
	webRegistration, err := writer.Admit(ctx, webInput)
	if err != nil {
		t.Fatalf("approve web template: %v", err)
	}
	assertApprovedAuthorityRegistration(t, webRegistration)

	t.Run("receipt attempt release and policy pin one exact authority decision", func(t *testing.T) {
		attempt := apiRegistration.Attempt
		receipt := apiRegistration.AuthorityReceipt
		release := apiRegistration.Release.Release.Snapshot()
		policy := apiRegistration.Release.Policy
		if receipt == nil || attempt.AuthorityReceipt == nil || release.AuthorityReceipt == nil || policy.AuthorityReceipt == nil {
			t.Fatalf("authority lineage is incomplete: %#v", apiRegistration)
		}
		if attempt.SchemaVersion != AdmissionAttemptSchemaVersionV2 || release.SchemaVersion != TemplateReleaseSchemaVersionV2 ||
			policy.SchemaVersion != ReleasePolicySchemaVersionV2 {
			t.Fatalf("authority admission did not produce only v2 documents: attempt=%s release=%s policy=%s",
				attempt.SchemaVersion, release.SchemaVersion, policy.SchemaVersion)
		}
		exact := ArtifactAuthorityReceiptRef{ID: receipt.ID, ContentHash: receipt.ContentHash, PolicyHash: receipt.PolicyHash}
		if *attempt.AuthorityReceipt != exact || *release.AuthorityReceipt != exact || *policy.AuthorityReceipt != exact {
			t.Fatalf("receipt exact reference drifted across lineage: receipt=%#v attempt=%#v release=%#v policy=%#v",
				exact, attempt.AuthorityReceipt, release.AuthorityReceipt, policy.AuthorityReceipt)
		}
		if receipt.SubjectHash != release.SubjectHash || receipt.SourceTreeHash != release.Source.TreeHash ||
			receipt.SBOMDigest != release.SBOMDigest || receipt.SignatureBundleDigest != release.Signature.BundleDigest {
			t.Fatalf("authority receipt does not bind exact release material: %#v", receipt)
		}

		var status, attemptSchema, releaseSchema, policySchema string
		var version uint64
		var attemptReceiptID, releaseReceiptID, policyReceiptID string
		if err := database.QueryRowContext(ctx, `
SELECT a.status, a.version, a.schema_version, r.schema_version, p.schema_version,
       a.authority_receipt_id::text, r.authority_receipt_id::text, p.authority_receipt_id::text
FROM template_admission_attempts AS a
JOIN template_releases AS r ON r.admission_attempt_id = a.id
JOIN template_release_policies AS p ON p.template_release_id = r.id
WHERE a.id = $1
`, apiInput.AttemptID).Scan(
			&status, &version, &attemptSchema, &releaseSchema, &policySchema,
			&attemptReceiptID, &releaseReceiptID, &policyReceiptID,
		); err != nil {
			t.Fatal(err)
		}
		if status != AttemptApproved.String() || version != 3 || attemptSchema != AdmissionAttemptSchemaVersionV2 ||
			releaseSchema != TemplateReleaseSchemaVersionV2 || policySchema != ReleasePolicySchemaVersionV2 ||
			attemptReceiptID != exact.ID || releaseReceiptID != exact.ID || policyReceiptID != exact.ID {
			t.Fatalf("unexpected persisted authority lineage: status=%s version=%d schemas=%s/%s/%s receipts=%s/%s/%s",
				status, version, attemptSchema, releaseSchema, policySchema, attemptReceiptID, releaseReceiptID, policyReceiptID)
		}

		registry, err := NewRegistry(gormDB)
		if err != nil {
			t.Fatal(err)
		}
		loaded, err := registry.GetTemplateReleaseExact(ctx, exactTemplateReleaseRef(apiRegistration.Release.Release))
		if err != nil {
			t.Fatal(err)
		}
		if !authorityBoundSelectableRegistration(loaded) || loaded.AuthorityReceipt == nil || loaded.AuthorityReceipt.Ref() != exact {
			t.Fatalf("registry did not hydrate the exact authority-bound release: %#v", loaded)
		}
	})

	apiRelease := apiRegistration.Release.Release
	webRelease := webRegistration.Release.Release
	t.Run("full stack exact registration and role rejection", func(t *testing.T) {
		input := writerFullStackInput(uuid.NewString(), "writer-full-stack", apiRelease, webRelease)
		registration, err := writer.RegisterFullStack(ctx, input)
		if err != nil {
			t.Fatal(err)
		}
		view := registration.Template.Snapshot()
		if len(registration.Components) != 2 || len(view.Components) != 2 {
			t.Fatalf("full-stack registration lost exact components: %#v", registration)
		}
		registry, err := NewRegistry(gormDB)
		if err != nil {
			t.Fatal(err)
		}
		resolved, err := registry.ResolveForNewBuild(ctx, ExactFullStackTemplateRef{ID: view.ID, ContentHash: view.ContentHash})
		if err != nil {
			t.Fatal(err)
		}
		if len(resolved.Components) != 2 {
			t.Fatalf("registered full stack did not resolve exactly: %#v", resolved)
		}

		invalidInput := writerFullStackInput(uuid.NewString(), "writer-role-mismatch", apiRelease, webRelease)
		invalidInput.Components[0].Role = "web"
		invalidInput.Components[1].Role = "api"
		if _, err := writer.RegisterFullStack(ctx, invalidInput); !errors.Is(err, ErrInvalidTemplate) {
			t.Fatalf("component role mismatch was not rejected: %v", err)
		}
		if got := writerRowCount(t, ctx, database, "full_stack_template_releases", "id = $1", invalidInput.ID); got != 0 {
			t.Fatalf("role mismatch wrote %d full-stack documents", got)
		}
	})

	t.Run("full stack rollback is atomic", func(t *testing.T) {
		if _, err := database.ExecContext(ctx, `
CREATE FUNCTION writer_fail_web_component()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  IF NEW.role = 'web' THEN
    RAISE EXCEPTION 'writer test component failure';
  END IF;
  RETURN NEW;
END;
$$;
CREATE TRIGGER zz_writer_fail_web_component
BEFORE INSERT ON full_stack_template_components
FOR EACH ROW EXECUTE FUNCTION writer_fail_web_component();
`); err != nil {
			t.Fatal(err)
		}
		input := writerFullStackInput(uuid.NewString(), "writer-atomic-full-stack", apiRelease, webRelease)
		if _, err := writer.RegisterFullStack(ctx, input); err == nil {
			t.Fatal("injected component failure did not fail registration")
		}
		if got := writerRowCount(t, ctx, database, "full_stack_template_releases", "id = $1", input.ID); got != 0 {
			t.Fatalf("failed full-stack transaction retained %d documents", got)
		}
		if got := writerRowCount(t, ctx, database, "full_stack_template_components", "full_stack_template_id = $1", input.ID); got != 0 {
			t.Fatalf("failed full-stack transaction retained %d components", got)
		}
		if _, err := database.ExecContext(ctx, `
DROP TRIGGER zz_writer_fail_web_component ON full_stack_template_components;
DROP FUNCTION writer_fail_web_component();
`); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("non-approved release is not selectable", func(t *testing.T) {
		if _, err := database.ExecContext(ctx, `
UPDATE template_release_policies
SET state = 'deprecated', version = version + 1,
    reason = 'superseded during writer verification', updated_by = $1, updated_at = $2
WHERE template_release_id = $3
`, evaluatorID, baseTime.Add(2*time.Hour), apiRelease.ID()); err != nil {
			t.Fatal(err)
		}
		input := writerFullStackInput(uuid.NewString(), "writer-deprecated-full-stack", apiRelease, webRelease)
		if _, err := writer.RegisterFullStack(ctx, input); !errors.Is(err, ErrReleaseNotSelectable) {
			t.Fatalf("deprecated release remained selectable: %v", err)
		}
		if got := writerRowCount(t, ctx, database, "full_stack_template_releases", "id = $1", input.ID); got != 0 {
			t.Fatalf("non-approved release wrote %d full-stack documents", got)
		}
	})

	t.Run("admission rollback includes immutable receipt", func(t *testing.T) {
		if _, err := database.ExecContext(ctx, `
CREATE FUNCTION writer_fail_policy_insert()
RETURNS trigger
LANGUAGE plpgsql
AS $$
BEGIN
  RAISE EXCEPTION 'writer test policy failure';
END;
$$;
CREATE TRIGGER zz_writer_fail_policy_insert
BEFORE INSERT ON template_release_policies
FOR EACH ROW EXECUTE FUNCTION writer_fail_policy_insert();
`); err != nil {
			t.Fatal(err)
		}
		input := writerAdmissionInput(uuid.NewString(), uuid.NewString(), validCandidate("writer-atomic-admission", "api"))
		if _, err := writer.Admit(ctx, input); err == nil {
			t.Fatal("injected policy failure did not fail admission")
		}
		if got := writerRowCount(t, ctx, database, "template_admission_attempts", "id = $1", input.AttemptID); got != 0 {
			t.Fatalf("failed admission transaction retained %d attempts", got)
		}
		if got := writerRowCount(t, ctx, database, "template_releases", "id = $1", input.ReleaseID); got != 0 {
			t.Fatalf("failed admission transaction retained %d releases", got)
		}
		if got := writerRowCount(t, ctx, database, "template_release_policies", "template_release_id = $1", input.ReleaseID); got != 0 {
			t.Fatalf("failed admission transaction retained %d policies", got)
		}
		receiptID := deterministicAuthorityReceiptID(input.Candidate.Manifest.TemplateID)
		if got := writerRowCount(t, ctx, database, "template_artifact_authority_receipts", "id = $1", receiptID); got != 0 {
			t.Fatalf("failed admission transaction retained %d authority receipts", got)
		}
	})
}

type fakeArtifactAuthority struct {
	readinessErr  error
	verifyErr     error
	mutateReceipt func(*NewArtifactAuthorityReceiptInput)
	verifyCalls   int
	requests      []ArtifactAuthorityVerifyRequest
}

func (a *fakeArtifactAuthority) Readiness(context.Context) error {
	return a.readinessErr
}

func (a *fakeArtifactAuthority) Verify(_ context.Context, request ArtifactAuthorityVerifyRequest) (ArtifactAuthorityReceipt, error) {
	a.verifyCalls++
	a.requests = append(a.requests, request)
	if a.verifyErr != nil {
		return ArtifactAuthorityReceipt{}, a.verifyErr
	}
	admissionBase := baseTime.Add(time.Duration(a.verifyCalls-1) * 10 * time.Minute)
	verifiedAt := admissionBase.Add(2 * time.Minute)
	receiptInput := NewArtifactAuthorityReceiptInput{
		ID:                    deterministicAuthorityReceiptID(request.Candidate.Manifest.TemplateID),
		SubjectHash:           request.SubjectHash,
		SourceTreeHash:        request.Candidate.Source.TreeHash,
		ArtifactDigest:        digest("4"),
		SBOMDigest:            request.Candidate.SBOMDigest,
		SignatureBundleDigest: digest("3"),
		PolicyHash:            digest("5"),
		Authority: ArtifactAuthorityIdentity{
			ID: "template-artifact-authority.test", Version: "1.0.0-test",
		},
		VerifierImageDigest: digest("6"),
		TrustRootDigest:     digest("7"),
		TransparencyLog: ArtifactTransparencyLog{
			ID: "rekor.test", EntryUUID: "entry-" + strings.ReplaceAll(request.Candidate.Manifest.TemplateID, "-", "."),
			LogIndex: int64(a.verifyCalls - 1), IntegratedAt: admissionBase.Add(time.Minute),
		},
		VerificationReference: "urn:artifact-authority:verification:" + request.Candidate.Manifest.TemplateID,
		ArtifactDescriptor: ArtifactDescriptor{
			Reference: request.Bundle.ArtifactReference,
			MediaType: "application/vnd.oci.image.manifest.v1+json", Digest: digest("4"), SizeBytes: 100,
			Config: ArtifactBlobDescriptor{
				MediaType: "application/vnd.oci.image.config.v1+json", Digest: digest("a"), SizeBytes: 20,
			},
			Layers: []ArtifactBlobDescriptor{{
				MediaType: "application/vnd.oci.image.layer.v1.tar+gzip", Digest: digest("b"), SizeBytes: 30,
			}},
			TotalBytes: 150,
		},
		SBOMDescriptor: ArtifactSBOMDescriptor{
			SchemaVersion: "worksflow.template-sbom-aggregate/v1", Digest: request.Candidate.SBOMDigest, ServiceCount: 1,
			Services: []ArtifactSBOMServiceDescriptor{{
				ServiceID:      request.Bundle.ServiceSBOMs[0].ServiceID,
				ImageReference: request.Bundle.ServiceSBOMs[0].ImageReference, ImageDigest: digest("8"),
				ReferrerReference: request.Bundle.ServiceSBOMs[0].ReferrerReference, ReferrerDigest: digest("9"),
				StatementDigest: digest("a"), PredicateDigest: digest("b"), SPDXVersion: "SPDX-2.3",
				DocumentNamespace: "https://spdx.test/" + request.Candidate.Manifest.TemplateID,
				EvidenceHash:      digest("c"),
			}},
		},
		Proof: ArtifactAuthorityProof{
			PayloadType: "application/vnd.in-toto+json", PredicateType: "https://slsa.dev/provenance/v1",
			PayloadDigest: digest("a"), SignatureBundleDigest: digest("3"),
			SignerIdentities:         []string{"https://github.com/ai-worksflow/templates/.github/workflows/admit.yml@refs/heads/main"},
			TransparencyBundleDigest: digest("b"), LogID: "rekor.test",
			EntryUUID: "entry-" + strings.ReplaceAll(request.Candidate.Manifest.TemplateID, "-", "."),
			LogIndex:  int64(a.verifyCalls - 1), TreeSize: uint64(a.verifyCalls + 10), RootHash: digest("c"),
			IntegratedAt: admissionBase.Add(time.Minute), CheckpointSignedAt: verifiedAt,
		},
		Evidence:   validEvidence(request.SubjectHash, verifiedAt),
		Signature:  validSignature(request.SubjectHash, verifiedAt),
		VerifiedAt: verifiedAt,
		RecordedBy: request.RecordedBy,
		CreatedAt:  verifiedAt,
	}
	if a.mutateReceipt != nil {
		a.mutateReceipt(&receiptInput)
	}
	return NewArtifactAuthorityReceipt(receiptInput)
}

func writerAdmissionInput(attemptIDValue, releaseIDValue string, candidate AdmissionCandidate) AdmitInput {
	return AdmitInput{
		AttemptID: attemptIDValue,
		ReleaseID: releaseIDValue,
		Candidate: candidate,
		Bundle: ArtifactAdmissionBundle{
			ArtifactReference: "ghcr.io/worksflow/templates/" + candidate.Manifest.TemplateID + "@" + digest("4"),
			ServiceSBOMs: []ArtifactServiceSBOMReference{{
				ServiceID:         candidate.Manifest.Services[0].ID,
				ImageReference:    "ghcr.io/worksflow/templates/" + candidate.Manifest.TemplateID + "-service@" + digest("8"),
				ReferrerReference: "ghcr.io/worksflow/templates/" + candidate.Manifest.TemplateID + "-sbom@" + digest("9"),
			}},
			DSSEEnvelope:          []byte(`{"payloadType":"application/vnd.in-toto+json","payload":"test","signatures":[]}`),
			TransparencyBundle:    []byte(`{"kind":"rekorInclusionProof","test":true}`),
			VerificationReference: "urn:artifact-authority:bundle:" + candidate.Manifest.TemplateID,
		},
		RequestedBy: actorID,
		EvaluatedBy: evaluatorID,
	}
}

func deterministicAuthorityReceiptID(templateID string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("worksflow-template-authority:"+templateID)).String()
}

func configureDeterministicWriterClock(writer *Writer) {
	call := 0
	writer.now = func() time.Time {
		admission := call / 3
		phase := call % 3
		call++
		return baseTime.Add(time.Duration(admission*10+phase) * time.Minute)
	}
}

func writerFullStackInput(id, templateID string, apiRelease, webRelease TemplateRelease) RegisterFullStackInput {
	return RegisterFullStackInput{
		ID: id, TemplateID: templateID, Version: "1.0.0",
		Components: []FullStackComponentSelection{
			{Role: "api", MountPath: "services/api", Release: exactTemplateReleaseRef(apiRelease)},
			{Role: "web", MountPath: "apps/web", Release: exactTemplateReleaseRef(webRelease)},
		},
		Layout: FullStackLayout{
			ContractTruthSource: "openapi", OpenAPIPath: "contracts/openapi.yaml",
			GeneratedClientPath: "packages/api-client", DeploymentPath: "deployment",
			TestPath: "tests", DatabaseEngine: "postgresql",
		},
		CreatedBy: actorID, CreatedAt: baseTime.Add(3 * time.Hour),
	}
}

func exactTemplateReleaseRef(release TemplateRelease) TemplateReleaseRef {
	return TemplateReleaseRef{ID: release.ID(), ContentHash: release.ContentHash(), SubjectHash: release.SubjectHash()}
}

func assertApprovedAuthorityRegistration(t *testing.T, registration AdmissionRegistration) {
	t.Helper()
	if registration.Attempt.Status != AttemptApproved || registration.Attempt.Version != 3 || registration.Release == nil || registration.AuthorityReceipt == nil {
		t.Fatalf("unexpected approved authority registration: %#v", registration)
	}
	if registration.Release.Policy.State != ReleaseApproved || registration.Release.Policy.Version != 1 {
		t.Fatalf("approved release has no initial approved policy: %#v", registration.Release.Policy)
	}
	if registration.Attempt.ApprovedReleaseID != registration.Release.Release.ID() {
		t.Fatal("admission result does not point at its immutable release")
	}
	if registration.Attempt.AuthorityReceipt == nil || registration.Release.Release.Snapshot().AuthorityReceipt == nil ||
		registration.Release.Policy.AuthorityReceipt == nil {
		t.Fatal("approved v2 registration lost its authority receipt reference")
	}
}

func setupWriterPostgres(t *testing.T) (context.Context, *gorm.DB, *sql.DB) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	t.Cleanup(cancel)
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = base.Close() })
	schema := "template_writer_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := base.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = base.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`)
	})
	database, err := sql.Open("pgx", writerPostgresDSNWithSearchPath(t, dsn, schema))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if err := platformmigrations.Up(ctx, database); err != nil {
		t.Fatalf("migrations.Up failed in temporary schema: %v", err)
	}
	gormDB, err := gorm.Open(postgres.New(postgres.Config{Conn: database}), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ExecContext(ctx, `
INSERT INTO users (id, email, display_name, password_hash) VALUES
  ($1, $2, 'Template requester', 'not-used'),
  ($3, $4, 'Template evaluator', 'not-used')
`, actorID, "writer-requester-"+uuid.NewString()+"@example.com", evaluatorID, "writer-evaluator-"+uuid.NewString()+"@example.com"); err != nil {
		t.Fatal(err)
	}
	return ctx, gormDB, database
}

func writerRowCount(t *testing.T, ctx context.Context, database *sql.DB, table, predicate string, argument any) int {
	t.Helper()
	var count int
	query := "SELECT count(*) FROM " + table + " WHERE " + predicate
	if err := database.QueryRowContext(ctx, query, argument).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func writerPostgresDSNWithSearchPath(t *testing.T, dsn, schema string) string {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err == nil && parsed.Scheme != "" {
		query := parsed.Query()
		query.Set("search_path", schema)
		parsed.RawQuery = query.Encode()
		return parsed.String()
	}
	return fmt.Sprintf("%s search_path=%s", strings.TrimSpace(dsn), schema)
}
