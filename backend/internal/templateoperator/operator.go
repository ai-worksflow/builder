package templateoperator

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/worksflow/builder/backend/internal/repository"
	"github.com/worksflow/builder/backend/internal/templateauthority"
	"github.com/worksflow/builder/backend/internal/templates"
	"gorm.io/gorm"
)

const (
	requiredAuthorityMigration = "000055_template_artifact_authority_receipts"
	maxAdmissionRequestBytes   = 32 << 20
)

type EnvironmentLookup func(string) (string, bool)

// AdmissionRequest is the complete mutable input accepted by the operator.
// Status, evidence, signatures, policy state, trust roots, clocks, and receipt
// identities are deliberately absent or nested only as signed raw bundles.
type AdmissionRequest struct {
	SchemaVersion string                            `json:"schemaVersion"`
	AttemptID     string                            `json:"attemptId"`
	ReleaseID     string                            `json:"releaseId"`
	Candidate     templates.AdmissionCandidate      `json:"candidate"`
	Bundle        templates.ArtifactAdmissionBundle `json:"bundle"`
	RequestedBy   string                            `json:"requestedBy"`
	EvaluatedBy   string                            `json:"evaluatedBy"`
}

type Operator struct {
	database    *gorm.DB
	authority   *templates.VerifiedArtifactAuthority
	writer      *templates.Writer
	commitments Commitments
}

func DecodeAdmissionRequest(encoded []byte) (AdmissionRequest, error) {
	if len(encoded) == 0 || len(encoded) > maxAdmissionRequestBytes {
		return AdmissionRequest{}, fmt.Errorf("Template admission request must be between 1 and %d bytes", maxAdmissionRequestBytes)
	}
	var request AdmissionRequest
	if err := decodeStrictJSON(encoded, &request); err != nil {
		return AdmissionRequest{}, fmt.Errorf("decode Template admission request: %w", err)
	}
	if request.SchemaVersion != AdmissionSchemaVersion {
		return AdmissionRequest{}, fmt.Errorf("schemaVersion must equal %q", AdmissionSchemaVersion)
	}
	return request, nil
}

// New constructs the complete operator-only authority. It derives policy and
// trust commitments from key bytes, requires their reviewed pins to match,
// resolves registry credentials only from server environment variables, and
// exposes no unauthenticated HTTP write surface.
func New(database *gorm.DB, config Config, lookup EnvironmentLookup) (*Operator, error) {
	if database == nil {
		return nil, errors.New("Template Artifact Authority database is required")
	}
	if lookup == nil {
		return nil, errors.New("Template Artifact Authority environment lookup is required")
	}
	compiled, err := compileConfig(config)
	if err != nil {
		return nil, err
	}
	if !digestPattern.MatchString(config.Authority.ExpectedPolicyHash) ||
		!digestPattern.MatchString(config.Authority.ExpectedTrustRootDigest) {
		return nil, errors.New("reviewed expected policy and trust-root digests are required")
	}
	if config.Authority.ExpectedPolicyHash != compiled.commitments.PolicyHash ||
		config.Authority.ExpectedTrustRootDigest != compiled.commitments.TrustRootDigest {
		return nil, errors.New("reviewed Artifact Authority commitments do not match the configured policy and trust material")
	}

	source, err := repository.NewGitTemplateSourceMaterializer(repository.GitTemplateSourceOptions{
		GitBinary: compiled.config.Source.GitBinary, CacheRoot: compiled.config.Source.CacheRoot,
		AllowedHosts: compiled.config.Source.AllowedHosts, FetchTimeout: compiled.sourceTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("configure exact Git source verifier: %w", err)
	}

	httpOrigins := make([]templateauthority.RegistryHTTPOrigin, 0, len(compiled.config.Registry.Origins))
	for _, origin := range compiled.config.Registry.Origins {
		authorization := ""
		if origin.AuthorizationEnv != "" {
			value, present := lookup(origin.AuthorizationEnv)
			if !present || strings.TrimSpace(value) == "" {
				return nil, fmt.Errorf("registry credential environment variable %s is required", origin.AuthorizationEnv)
			}
			authorization = value
		}
		httpOrigins = append(httpOrigins, templateauthority.RegistryHTTPOrigin{
			Host: origin.Host, Authorization: authorization, RedirectHosts: origin.RedirectHosts,
		})
	}
	registryClient, err := templateauthority.NewHTTPSRegistryClient(templateauthority.HTTPSRegistryClientConfig{
		Origins: httpOrigins, Timeout: compiled.registryTimeout, MaxRedirects: compiled.config.Registry.MaxRedirects,
	})
	if err != nil {
		return nil, fmt.Errorf("configure HTTPS OCI Registry client: %w", err)
	}
	repositories := make([]templateauthority.RepositoryRule, 0, len(compiled.config.Registry.Repositories))
	for _, repositoryConfig := range compiled.config.Registry.Repositories {
		repositories = append(repositories, templateauthority.RepositoryRule{
			Host: repositoryConfig.Host, Repository: repositoryConfig.Repository,
		})
	}
	oci, err := templateauthority.NewOCIVerifier(registryClient, templateauthority.RegistryPolicy{
		Repositories: repositories, RedirectHosts: compiled.redirectHosts,
	}, templateauthority.Limits{
		MaxManifestBytes: compiled.config.Registry.MaxManifestBytes,
		MaxBlobBytes:     compiled.config.Registry.MaxBlobBytes,
		MaxTotalBytes:    compiled.config.Registry.MaxTotalBytes,
		MaxBlobs:         compiled.config.Registry.MaxBlobs,
		MaxRedirects:     compiled.config.Registry.MaxRedirects,
		Timeout:          compiled.registryTimeout,
	})
	if err != nil {
		return nil, fmt.Errorf("configure OCI verifier: %w", err)
	}
	sbom, err := templateauthority.NewSBOMVerifier(oci)
	if err != nil {
		return nil, fmt.Errorf("configure SBOM verifier: %w", err)
	}
	dsse, err := templateauthority.NewDSSEVerifier(compiled.dssePolicy)
	if err != nil {
		return nil, fmt.Errorf("configure DSSE verifier: %w", err)
	}
	transparency, err := templateauthority.NewTransparencyVerifier(compiled.transparencyPolicy)
	if err != nil {
		return nil, fmt.Errorf("configure transparency verifier: %w", err)
	}
	authority, err := templates.NewVerifiedArtifactAuthority(templates.VerifiedArtifactAuthorityConfig{
		AuthorityID: compiled.config.Authority.ID, AuthorityVersion: compiled.config.Authority.Version,
		VerifierImageDigest: compiled.config.Authority.VerifierImageDigest,
		PolicyHash:          compiled.commitments.PolicyHash, TrustRootDigest: compiled.commitments.TrustRootDigest,
		PredicateType: compiled.config.Authority.PredicateType,
		Source:        source, OCI: oci, SBOM: sbom, DSSE: dsse, Transparency: transparency,
		DependencyReadiness: registryClient.Readiness,
	})
	if err != nil {
		return nil, fmt.Errorf("configure Template Artifact Authority: %w", err)
	}
	writer, err := templates.NewWriter(database, authority)
	if err != nil {
		return nil, err
	}
	return &Operator{database: database, authority: authority, writer: writer, commitments: compiled.commitments}, nil
}

func (operator *Operator) Commitments() Commitments {
	if operator == nil {
		return Commitments{}
	}
	return operator.commitments
}

// Readiness fails closed unless PostgreSQL is reachable at migration 55 or
// newer and all source/registry trust dependencies pass their real probes.
func (operator *Operator) Readiness(ctx context.Context) error {
	if operator == nil || operator.database == nil || operator.authority == nil || operator.writer == nil {
		return errors.New("Template Artifact Authority operator is not configured")
	}
	if ctx == nil {
		return errors.New("Template Artifact Authority readiness context is required")
	}
	sqlDatabase, err := operator.database.DB()
	if err != nil {
		return fmt.Errorf("open Template Artifact Authority database: %w", err)
	}
	if err := sqlDatabase.PingContext(ctx); err != nil {
		return fmt.Errorf("Template Artifact Authority database readiness: %w", err)
	}
	var migrationApplied bool
	var receiptTablePresent bool
	if err := operator.database.WithContext(ctx).Raw(`
SELECT
  EXISTS (
    SELECT 1 FROM schema_migrations WHERE version = ?
  ),
  to_regclass('template_artifact_authority_receipts') IS NOT NULL
`, requiredAuthorityMigration).Row().Scan(&migrationApplied, &receiptTablePresent); err != nil {
		return fmt.Errorf("inspect Template Artifact Authority schema: %w", err)
	}
	if !migrationApplied || !receiptTablePresent {
		return fmt.Errorf("Template Artifact Authority requires migration %s", requiredAuthorityMigration)
	}
	if err := operator.authority.Readiness(ctx); err != nil {
		return err
	}
	return nil
}

func (operator *Operator) Admit(ctx context.Context, request AdmissionRequest) (templates.AdmissionRegistration, error) {
	if request.SchemaVersion != AdmissionSchemaVersion {
		return templates.AdmissionRegistration{}, fmt.Errorf("schemaVersion must equal %q", AdmissionSchemaVersion)
	}
	if err := operator.Readiness(ctx); err != nil {
		return templates.AdmissionRegistration{}, err
	}
	return operator.writer.Admit(ctx, templates.AdmitInput{
		AttemptID: request.AttemptID, ReleaseID: request.ReleaseID,
		Candidate: request.Candidate, Bundle: request.Bundle,
		RequestedBy: request.RequestedBy, EvaluatedBy: request.EvaluatedBy,
	})
}
