package templates

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
	platformmigrations "github.com/worksflow/builder/backend/migrations"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestRegistryHydrationAlwaysRevalidatesCanonicalReleaseAndPolicy(t *testing.T) {
	attempt, release := registryApprovedPair(t, attemptID, releaseID, validCandidate("api-template", "api"))
	policy, err := NewReleasePolicy(release, evaluatorID, baseTime.Add(4*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	releaseModel := registryReleaseModel(t, release)
	policyModel := registryPolicyModel(policy)
	registration, err := hydrateTemplateReleaseRegistration(releaseModel, policyModel)
	if err != nil {
		t.Fatal(err)
	}
	if registration.Release.ContentHash() != release.ContentHash() || registration.Policy.State != ReleaseApproved {
		t.Fatalf("unexpected hydrated registration: %#v", registration)
	}
	if attempt.Snapshot().ApprovedReleaseID != registration.Release.ID() {
		t.Fatal("fixture admission lineage does not match hydrated release")
	}

	t.Run("tampered immutable payload", func(t *testing.T) {
		corrupt := releaseModel
		corrupt.ContentHash = digest("9")
		if _, err := hydrateTemplateReleaseRegistration(corrupt, policyModel); err == nil {
			t.Fatal("release content/hash drift was accepted")
		}
	})
	t.Run("index manifest drift", func(t *testing.T) {
		corrupt := releaseModel
		corrupt.TemplateID = "different-template"
		if _, err := hydrateTemplateReleaseRegistration(corrupt, policyModel); err == nil {
			t.Fatal("release index/manifest drift was accepted")
		}
	})
	t.Run("policy exact hash drift", func(t *testing.T) {
		corrupt := policyModel
		corrupt.ReleaseContentHash = digest("8")
		if _, err := hydrateTemplateReleaseRegistration(releaseModel, corrupt); err == nil {
			t.Fatal("policy/release hash drift was accepted")
		}
	})
	t.Run("post-transition approved policy", func(t *testing.T) {
		corrupt := policyModel
		corrupt.Version = 2
		if _, err := hydrateTemplateReleaseRegistration(releaseModel, corrupt); err == nil {
			t.Fatal("impossible approved policy version was accepted")
		}
	})
}

func TestRegistryKeepsLegacyV1ReadableButNeverAuthoritySelectable(t *testing.T) {
	attempt, release := registryApprovedPair(t, attemptID, releaseID, validCandidate("legacy-api-template", "api"))
	policy, err := NewReleasePolicy(release, evaluatorID, baseTime.Add(4*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	policy, err = policy.Transition(1, ReleaseRevoked, "historical release has no Artifact Authority receipt", evaluatorID, baseTime.Add(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	registration, err := hydrateTemplateReleaseRegistration(registryReleaseModel(t, release), registryPolicyModel(policy))
	if err != nil {
		t.Fatalf("legacy v1 release is no longer readable for audit: %v", err)
	}
	if registration.Release.Snapshot().SchemaVersion != TemplateReleaseSchemaVersion ||
		registration.Policy.SchemaVersion != ReleasePolicySchemaVersion || registration.Policy.State != ReleaseRevoked {
		t.Fatalf("unexpected historical registration: %#v", registration)
	}
	if registration.AuthorityReceipt != nil || authorityBoundSelectableRegistration(registration) {
		t.Fatalf("legacy release became authority-selectable: %#v", registration)
	}
	if attempt.Snapshot().SchemaVersion != AdmissionAttemptSchemaVersion {
		t.Fatal("legacy fixture unexpectedly used a v2 admission")
	}
}

func TestRegistryFullStackHydrationRejectsRelationalComponentDrift(t *testing.T) {
	_, apiRelease := registryApprovedPair(t, attemptID, releaseID, validCandidate("api-template", "api"))
	_, webRelease := registryApprovedPair(t, secondAttempt, secondRelease, validCandidate("web-template", "web"))
	stack := registryFullStack(t, apiRelease, webRelease)
	model := registryFullStackModel(t, stack)
	hydrated, err := hydrateFullStackTemplate(*model)
	if err != nil {
		t.Fatal(err)
	}
	rows := registryFullStackComponentModels(stack)
	components, err := validateFullStackComponentRows(hydrated, rows)
	if err != nil {
		t.Fatal(err)
	}
	if len(components) != 2 || components[0].Role != "api" || components[1].Role != "web" {
		t.Fatalf("unexpected canonical components: %#v", components)
	}

	tests := []struct {
		name   string
		mutate func([]fullStackTemplateComponentModel) []fullStackTemplateComponentModel
	}{
		{"missing component", func(values []fullStackTemplateComponentModel) []fullStackTemplateComponentModel { return values[:1] }},
		{"content hash drift", func(values []fullStackTemplateComponentModel) []fullStackTemplateComponentModel {
			values[0].TemplateReleaseContentHash = digest("7")
			return values
		}},
		{"role drift", func(values []fullStackTemplateComponentModel) []fullStackTemplateComponentModel {
			values[0].Role = "worker"
			return values
		}},
		{"mount drift", func(values []fullStackTemplateComponentModel) []fullStackTemplateComponentModel {
			values[0].MountPath = "unexpected"
			return values
		}},
		{"full-stack hash drift", func(values []fullStackTemplateComponentModel) []fullStackTemplateComponentModel {
			values[0].FullStackContentHash = digest("6")
			return values
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			copy := append([]fullStackTemplateComponentModel(nil), rows...)
			if _, err := validateFullStackComponentRows(hydrated, test.mutate(copy)); err == nil {
				t.Fatal("relational component drift was accepted")
			}
		})
	}
}

func TestRegistryListOptionsAreBoundedAndFailClosed(t *testing.T) {
	if _, err := normalizeRegistryLimit(maxRegistryListLimit + 1); err == nil {
		t.Fatal("unbounded registry list limit was accepted")
	}
	states, err := normalizeRegistryStates([]ReleasePolicyState{ReleaseApproved, ReleaseApproved, ReleaseRevoked})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(states, ",") != "approved,revoked" {
		t.Fatalf("states were not canonicalized: %#v", states)
	}
	if _, err := normalizeRegistryStates([]ReleasePolicyState{"selectable"}); err == nil {
		t.Fatal("unknown policy state was accepted")
	}
	if _, err := normalizeRegistryTemplateID("../template"); err == nil {
		t.Fatal("unsafe template ID was accepted")
	}
}

func TestRegistryPostgresResolvesOnlyExactApprovedFullStack(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("WORKSFLOW_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("WORKSFLOW_TEST_POSTGRES_DSN is not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	base, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := "template_registry_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := base.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = base.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS "`+schema+`" CASCADE`)
	})
	database, err := sql.Open("pgx", registryPostgresDSNWithSearchPath(t, dsn, schema))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
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
`, actorID, "registry-requester-"+uuid.NewString()+"@example.com", evaluatorID, "registry-evaluator-"+uuid.NewString()+"@example.com"); err != nil {
		t.Fatal(err)
	}

	writer, err := NewWriter(gormDB, &fakeArtifactAuthority{})
	if err != nil {
		t.Fatal(err)
	}
	configureDeterministicWriterClock(writer)
	apiAdmission, err := writer.Admit(ctx, writerAdmissionInput(uuid.NewString(), uuid.NewString(), validCandidate("registry-api-template", "api")))
	if err != nil {
		t.Fatalf("admit API fixture: %v", err)
	}
	webAdmission, err := writer.Admit(ctx, writerAdmissionInput(uuid.NewString(), uuid.NewString(), validCandidate("registry-web-template", "web")))
	if err != nil {
		t.Fatalf("admit web fixture: %v", err)
	}
	apiRelease := apiAdmission.Release.Release
	webRelease := webAdmission.Release.Release
	stackRegistration, err := writer.RegisterFullStack(ctx, writerFullStackInput(uuid.NewString(), "registry-full-stack", apiRelease, webRelease))
	if err != nil {
		t.Fatalf("register v2 registry fixtures: %v", err)
	}
	stack := stackRegistration.Template

	registry, err := NewRegistry(gormDB)
	if err != nil {
		t.Fatal(err)
	}
	releases, err := registry.ListTemplateReleases(ctx, TemplateReleaseListOptions{States: []ReleasePolicyState{ReleaseApproved}, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(releases) != 2 {
		t.Fatalf("expected two approved releases, got %d", len(releases))
	}
	gotAPI, err := registry.GetTemplateReleaseExact(ctx, TemplateReleaseRef{ID: apiRelease.ID(), ContentHash: apiRelease.ContentHash(), SubjectHash: apiRelease.SubjectHash()})
	if err != nil {
		t.Fatal(err)
	}
	if gotAPI.Policy.State != ReleaseApproved {
		t.Fatalf("unexpected API policy: %#v", gotAPI.Policy)
	}
	fullStacks, err := registry.ListFullStackTemplates(ctx, FullStackTemplateListOptions{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(fullStacks) != 1 || len(fullStacks[0].Components) != 2 {
		t.Fatalf("unexpected full-stack registry result: %#v", fullStacks)
	}
	exactStack, err := registry.GetFullStackTemplateExact(ctx, ExactFullStackTemplateRef{ID: stack.ID(), ContentHash: stack.ContentHash()})
	if err != nil {
		t.Fatal(err)
	}
	if exactStack.Template.ContentHash() != stack.ContentHash() {
		t.Fatal("exact full-stack get returned a different immutable document")
	}
	resolved, err := registry.ResolveForNewBuild(ctx, ExactFullStackTemplateRef{ID: stack.ID(), ContentHash: stack.ContentHash()})
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.Components) != 2 {
		t.Fatalf("expected two resolved components, got %d", len(resolved.Components))
	}

	if err := gormDB.WithContext(ctx).Exec(`
UPDATE template_release_policies
SET state = 'deprecated', version = version + 1,
    reason = 'superseded in registry test', updated_by = ?, updated_at = ?
WHERE template_release_id = ?
`, evaluatorID, baseTime.Add(20*time.Minute), apiRelease.ID()).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := registry.ResolveForNewBuild(ctx, ExactFullStackTemplateRef{ID: stack.ID(), ContentHash: stack.ContentHash()}); !errors.Is(err, ErrReleaseNotSelectable) {
		t.Fatalf("deprecated component remained selectable: %v", err)
	}
	if _, err := registry.GetTemplateReleaseExact(ctx, TemplateReleaseRef{ID: apiRelease.ID(), ContentHash: digest("9"), SubjectHash: apiRelease.SubjectHash()}); !errors.Is(err, ErrRegistryNotFound) {
		t.Fatalf("wrong exact hash did not return not found: %v", err)
	}

	t.Run("historical v1 is readable but cannot register a full stack", func(t *testing.T) {
		legacyAttempt, legacyRelease := registryApprovedPair(t, uuid.NewString(), uuid.NewString(), validCandidate("legacy-registry-api", "api"))
		legacyPolicy, err := NewReleasePolicy(legacyRelease, evaluatorID, baseTime.Add(4*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		legacyPolicy, err = legacyPolicy.Transition(1, ReleaseRevoked, "revoked during Artifact Authority migration", evaluatorID, baseTime.Add(5*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		for _, table := range []string{"template_admission_attempts", "template_releases", "template_release_policies"} {
			if _, err := database.ExecContext(ctx, "ALTER TABLE "+table+" DISABLE TRIGGER USER"); err != nil {
				t.Fatalf("disable historical fixture trigger on %s: %v", table, err)
			}
		}
		if err := gormDB.WithContext(ctx).Transaction(func(transaction *gorm.DB) error {
			return persistRegistryRelease(transaction, legacyAttempt, legacyRelease, legacyPolicy)
		}); err != nil {
			t.Fatalf("persist historical v1 fixture: %v", err)
		}
		for _, table := range []string{"template_admission_attempts", "template_releases", "template_release_policies"} {
			if _, err := database.ExecContext(ctx, "ALTER TABLE "+table+" ENABLE TRIGGER USER"); err != nil {
				t.Fatalf("enable historical fixture trigger on %s: %v", table, err)
			}
		}

		loaded, err := registry.GetTemplateReleaseExact(ctx, exactTemplateReleaseRef(legacyRelease))
		if err != nil {
			t.Fatalf("historical v1 release is not readable: %v", err)
		}
		if loaded.Policy.State != ReleaseRevoked || loaded.AuthorityReceipt != nil || authorityBoundSelectableRegistration(loaded) {
			t.Fatalf("historical v1 release became selectable: %#v", loaded)
		}
		input := writerFullStackInput(uuid.NewString(), "legacy-component-stack", legacyRelease, webRelease)
		if _, err := writer.RegisterFullStack(ctx, input); !errors.Is(err, ErrReleaseNotSelectable) {
			t.Fatalf("historical v1 component was registered into a FullStack: %v", err)
		}
		if got := writerRowCount(t, ctx, database, "full_stack_template_releases", "id = $1", input.ID); got != 0 {
			t.Fatalf("legacy component created %d FullStack rows", got)
		}
	})
}

func registryApprovedPair(t *testing.T, attemptIDValue, releaseIDValue string, candidate AdmissionCandidate) (AdmissionAttempt, TemplateRelease) {
	t.Helper()
	attempt, err := NewAdmissionAttempt(attemptIDValue, actorID, candidate, baseTime)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err = attempt.BeginValidation(baseTime.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	subject := attempt.Snapshot().SubjectHash
	attempt, release, err := attempt.Complete(
		releaseIDValue,
		validEvidence(subject, baseTime.Add(2*time.Minute)),
		validSignature(subject, baseTime.Add(2*time.Minute)),
		evaluatorID,
		baseTime.Add(3*time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	if release == nil {
		t.Fatal("valid registry fixture was rejected")
	}
	return attempt, *release
}

func registryReleaseModel(t *testing.T, release TemplateRelease) templateReleaseModel {
	t.Helper()
	view := release.Snapshot()
	manifest, err := json.Marshal(view.Manifest)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := json.Marshal(view.EvidenceRefs)
	if err != nil {
		t.Fatal(err)
	}
	signature, err := json.Marshal(view.Signature)
	if err != nil {
		t.Fatal(err)
	}
	return templateReleaseModel{
		ID: uuid.MustParse(view.ID), SchemaVersion: view.SchemaVersion,
		AdmissionAttemptID: uuid.MustParse(view.AdmissionAttemptID),
		TemplateID:         view.Manifest.TemplateID, ReleaseVersion: view.Manifest.Version,
		SourceRepository: view.Source.Repository, SourceBranch: view.Source.Branch,
		SourceCommit: view.Source.Commit, TreeHash: view.Source.TreeHash,
		Manifest: manifest, SBOMDigest: view.SBOMDigest,
		LicenseExpression: view.LicenseExpression, LicenseDigest: view.LicenseDigest,
		EvidenceRefs: evidence, Signature: signature, SubjectHash: view.SubjectHash,
		ContentHash: view.ContentHash, ApprovedBy: uuid.MustParse(view.ApprovedBy),
		ApprovedAt: view.ApprovedAt, CreatedAt: view.ApprovedAt.Add(time.Second),
	}
}

func registryPolicyModel(policy ReleasePolicy) templateReleasePolicyModel {
	return templateReleasePolicyModel{
		SchemaVersion:      policy.SchemaVersion,
		TemplateReleaseID:  uuid.MustParse(policy.TemplateReleaseID),
		ReleaseContentHash: policy.ReleaseContentHash,
		State:              policy.State.String(), Version: policy.Version, Reason: policy.Reason,
		UpdatedBy: uuid.MustParse(policy.UpdatedBy), CreatedAt: policy.CreatedAt, UpdatedAt: policy.UpdatedAt,
	}
}

func registryFullStack(t *testing.T, apiRelease, webRelease TemplateRelease) FullStackTemplate {
	t.Helper()
	stack, err := NewFullStackTemplate(
		fullStackID, "ai-conversation-stack", "1.0.0",
		[]FullStackComponentInput{
			{Role: "web", MountPath: "apps/web", Release: webRelease},
			{Role: "api", MountPath: "services/api", Release: apiRelease},
		},
		FullStackLayout{
			ContractTruthSource: "openapi", OpenAPIPath: "contracts/openapi.yaml",
			GeneratedClientPath: "packages/api-client", DeploymentPath: "deployment",
			TestPath: "tests", DatabaseEngine: "postgresql",
		},
		actorID, baseTime.Add(10*time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	return stack
}

func registryFullStackModel(t *testing.T, stack FullStackTemplate) *fullStackTemplateReleaseModel {
	t.Helper()
	view := stack.Snapshot()
	document, err := json.Marshal(stack)
	if err != nil {
		t.Fatal(err)
	}
	return &fullStackTemplateReleaseModel{
		ID: uuid.MustParse(view.ID), SchemaVersion: view.SchemaVersion,
		TemplateID: view.TemplateID, ReleaseVersion: view.Version,
		Document: document, ContentHash: view.ContentHash,
		CreatedBy: uuid.MustParse(view.CreatedBy), CreatedAt: view.CreatedAt,
	}
}

func registryFullStackComponentModels(stack FullStackTemplate) []fullStackTemplateComponentModel {
	view := stack.Snapshot()
	result := make([]fullStackTemplateComponentModel, 0, len(view.Components))
	for _, component := range view.Components {
		result = append(result, fullStackTemplateComponentModel{
			FullStackTemplateID: uuid.MustParse(view.ID), FullStackContentHash: view.ContentHash,
			Role: component.Role, MountPath: component.MountPath,
			TemplateReleaseID:          uuid.MustParse(component.Release.ID),
			TemplateReleaseContentHash: component.Release.ContentHash,
		})
	}
	return result
}

func persistRegistryRelease(transaction *gorm.DB, attempt AdmissionAttempt, release TemplateRelease, policy ReleasePolicy) error {
	attemptView := attempt.Snapshot()
	source, _ := json.Marshal(attemptView.Candidate.Source)
	manifest, _ := json.Marshal(attemptView.Candidate.Manifest)
	evidence, _ := json.Marshal(attemptView.Evidence)
	signature, _ := json.Marshal(attemptView.Signature)
	findings, _ := json.Marshal(attemptView.Findings)
	if err := transaction.Exec(`
INSERT INTO template_admission_attempts (
  id, schema_version, status, version, source, manifest,
  sbom_digest, license_expression, license_digest, subject_hash,
  evidence, signature, findings, approved_release_id,
  requested_by, evaluated_by, created_at, updated_at, evaluated_at
) VALUES (?, ?, ?, ?, ?::jsonb, ?::jsonb, ?, ?, ?, ?, ?::jsonb, ?::jsonb, ?::jsonb, ?, ?, ?, ?, ?, ?)
`,
		attemptView.ID, attemptView.SchemaVersion, attemptView.Status, attemptView.Version,
		string(source), string(manifest), attemptView.Candidate.SBOMDigest,
		attemptView.Candidate.LicenseExpression, attemptView.Candidate.LicenseDigest,
		attemptView.SubjectHash, string(evidence), string(signature), string(findings),
		attemptView.ApprovedReleaseID, attemptView.RequestedBy, attemptView.EvaluatedBy,
		attemptView.CreatedAt, attemptView.UpdatedAt, attemptView.EvaluatedAt,
	).Error; err != nil {
		return err
	}
	model := registryReleaseModelForPersistence(release)
	if err := transaction.Create(&model).Error; err != nil {
		return err
	}
	policyModel := registryPolicyModel(policy)
	return transaction.Create(&policyModel).Error
}

func registryReleaseModelForPersistence(release TemplateRelease) templateReleaseModel {
	view := release.Snapshot()
	manifest, _ := json.Marshal(view.Manifest)
	evidence, _ := json.Marshal(view.EvidenceRefs)
	signature, _ := json.Marshal(view.Signature)
	return templateReleaseModel{
		ID: uuid.MustParse(view.ID), SchemaVersion: view.SchemaVersion,
		AdmissionAttemptID: uuid.MustParse(view.AdmissionAttemptID),
		TemplateID:         view.Manifest.TemplateID, ReleaseVersion: view.Manifest.Version,
		SourceRepository: view.Source.Repository, SourceBranch: view.Source.Branch,
		SourceCommit: view.Source.Commit, TreeHash: view.Source.TreeHash,
		Manifest: manifest, SBOMDigest: view.SBOMDigest,
		LicenseExpression: view.LicenseExpression, LicenseDigest: view.LicenseDigest,
		EvidenceRefs: evidence, Signature: signature,
		SubjectHash: view.SubjectHash, ContentHash: view.ContentHash,
		ApprovedBy: uuid.MustParse(view.ApprovedBy), ApprovedAt: view.ApprovedAt,
		CreatedAt: view.ApprovedAt.Add(time.Second),
	}
}

func registryPostgresDSNWithSearchPath(t *testing.T, dsn, schema string) string {
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
