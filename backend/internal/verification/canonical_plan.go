package verification

import (
	"encoding/json"
	"sort"

	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/repository"
)

const CanonicalPlanContentSchemaVersion = "canonical-verification-plan-content/v1"

type CanonicalPlanSubject struct {
	WorkspaceArtifactID  string `json:"workspaceArtifactId"`
	WorkspaceRevisionID  string `json:"workspaceRevisionId"`
	WorkspaceContentHash string `json:"workspaceContentHash"`
}

type CanonicalPlanContent struct {
	SchemaVersion     string                    `json:"schemaVersion"`
	Scope             Scope                     `json:"scope"`
	ProjectID         string                    `json:"projectId"`
	Subject           CanonicalPlanSubject      `json:"subject"`
	BuildManifest     repository.ExactReference `json:"buildManifest"`
	BuildContract     repository.ExactReference `json:"buildContract"`
	FullStackTemplate repository.ExactReference `json:"fullStackTemplate"`
	Profile           ProfileReference          `json:"verificationProfile"`
	TemplateReleases  []PlanTemplateRelease     `json:"templateReleases"`
	Dependencies      []PlanDependency          `json:"dependencies"`
	Checks            []PlanCheck               `json:"checks"`
	Obligations       []PlanObligation          `json:"obligations"`
	RuntimePolicy     PlanRuntimePolicy         `json:"runtimePolicy"`
}

type CompileCanonicalPlanInput struct {
	ProjectID         string
	Subject           CanonicalPlanSubject
	BuildManifest     repository.ExactReference
	BuildContract     repository.ExactReference
	FullStackTemplate repository.ExactReference
	Profile           VerificationProfileDocument
	TemplateReleases  []ResolvedTemplateRelease
	Oracles           []PlanOracle
	Obligations       []PlanObligation
}

type CompiledCanonicalPlan struct {
	Content  CanonicalPlanContent `json:"content"`
	PlanHash string               `json:"planHash"`
}

func (PlanCompiler) CompileCanonical(input CompileCanonicalPlanInput) (CompiledCanonicalPlan, error) {
	profile, images, err := normalizeVerificationProfile(input.Profile)
	if err != nil {
		return CompiledCanonicalPlan{}, err
	}
	subject, err := normalizeCanonicalPlanSubject(input.Subject)
	if err != nil || !validUUIDs(input.ProjectID) {
		return CompiledCanonicalPlan{}, planInvalid("canonical subject or project identity")
	}
	manifest, err := normalizeExactReference(input.BuildManifest, "build manifest")
	if err != nil {
		return CompiledCanonicalPlan{}, planInvalid(err.Error())
	}
	contract, err := normalizeExactReference(input.BuildContract, "build contract")
	if err != nil {
		return CompiledCanonicalPlan{}, planInvalid(err.Error())
	}
	fullStack, err := normalizeExactReference(input.FullStackTemplate, "full-stack template")
	if err != nil {
		return CompiledCanonicalPlan{}, planInvalid(err.Error())
	}
	releases, resolved, err := normalizeResolvedReleases(input.TemplateReleases, profile)
	if err != nil {
		return CompiledCanonicalPlan{}, err
	}
	dependencies, err := compilePlanDependencies(resolved, profile)
	if err != nil {
		return CompiledCanonicalPlan{}, err
	}
	if _, err := parseCandidateDependencyResolverPolicy(profile.NetworkPolicy); err != nil {
		return CompiledCanonicalPlan{}, planInvalid("dependency resolver network policy")
	}
	oracles, err := normalizePlanOracles(input.Oracles)
	if err != nil {
		return CompiledCanonicalPlan{}, err
	}
	obligations, requiredOracleIDs, oracleObligations, err := normalizePlanObligations(input.Obligations, oracles)
	if err != nil {
		return CompiledCanonicalPlan{}, err
	}
	checks, err := compileOracleChecks(
		oracles, obligations, requiredOracleIDs, oracleObligations, resolved, profile, images,
	)
	if err != nil {
		return CompiledCanonicalPlan{}, err
	}
	checks, err = appendBuiltInChecks(
		checks, profile.BuiltInChecks, oracles, requiredOracleIDs, oracleObligations, images, profile.CommandImageRoles,
	)
	if err != nil {
		return CompiledCanonicalPlan{}, err
	}
	if err := validateCheckDependencies(checks); err != nil {
		return CompiledCanonicalPlan{}, err
	}
	if err := validateCanonicalReleaseChecks(checks); err != nil {
		return CompiledCanonicalPlan{}, err
	}
	content := CanonicalPlanContent{
		SchemaVersion: CanonicalPlanContentSchemaVersion, Scope: ScopeCanonical,
		ProjectID: input.ProjectID, Subject: subject,
		BuildManifest: manifest, BuildContract: contract, FullStackTemplate: fullStack,
		Profile:          ProfileReference{ID: profile.ID, Version: profile.Version, ContentHash: profile.ProfileHash},
		TemplateReleases: releases, Dependencies: dependencies, Checks: checks, Obligations: obligations,
		RuntimePolicy: PlanRuntimePolicy{
			Limits: cloneJSONObject(profile.Limits), NetworkPolicy: cloneJSONObject(profile.NetworkPolicy),
			HiddenTestBundle: append(json.RawMessage(nil), profile.HiddenTestBundle...),
		},
	}
	hash, err := domain.CanonicalHash(content)
	if err != nil {
		return CompiledCanonicalPlan{}, planInvalid("canonical content")
	}
	return CompiledCanonicalPlan{Content: content, PlanHash: "sha256:" + hash}, nil
}

func ParseCanonicalPlan(content CanonicalPlanContent, expectedHash string) (CompiledCanonicalPlan, error) {
	hash, err := domain.CanonicalHash(content)
	if err != nil || expectedHash != "sha256:"+hash {
		return CompiledCanonicalPlan{}, planInvalid("canonical content hash")
	}
	if content.SchemaVersion != CanonicalPlanContentSchemaVersion || content.Scope != ScopeCanonical ||
		!validUUIDs(content.ProjectID) {
		return CompiledCanonicalPlan{}, planInvalid("canonical envelope")
	}
	if normalized, err := normalizeCanonicalPlanSubject(content.Subject); err != nil || normalized != content.Subject {
		return CompiledCanonicalPlan{}, planInvalid("canonical subject")
	}
	for field, reference := range map[string]repository.ExactReference{
		"build manifest": content.BuildManifest, "build contract": content.BuildContract,
		"full-stack template": content.FullStackTemplate,
	} {
		normalized, err := normalizeExactReference(reference, field)
		if err != nil || normalized != reference {
			return CompiledCanonicalPlan{}, planInvalid(field)
		}
	}
	profile, err := normalizeProfile(content.Profile)
	if err != nil || profile != content.Profile {
		return CompiledCanonicalPlan{}, planInvalid("verification profile")
	}
	if err := validatePersistedPlanTemplates(content.TemplateReleases); err != nil {
		return CompiledCanonicalPlan{}, err
	}
	if err := validatePersistedPlanDependencies(content.Dependencies, content.TemplateReleases, content.Profile); err != nil {
		return CompiledCanonicalPlan{}, err
	}
	obligationOracles, mustOracles, err := validatePersistedPlanObligations(content.Obligations)
	if err != nil {
		return CompiledCanonicalPlan{}, err
	}
	if err := validatePersistedPlanChecks(content.Checks, obligationOracles, mustOracles); err != nil {
		return CompiledCanonicalPlan{}, err
	}
	if err := validateCanonicalReleaseChecks(content.Checks); err != nil {
		return CompiledCanonicalPlan{}, err
	}
	if content.RuntimePolicy.Limits == nil || content.RuntimePolicy.NetworkPolicy == nil {
		return CompiledCanonicalPlan{}, planInvalid("canonical runtime policy")
	}
	if _, err := parseCandidateDependencyResolverPolicy(content.RuntimePolicy.NetworkPolicy); err != nil {
		return CompiledCanonicalPlan{}, planInvalid("dependency resolver network policy")
	}
	if _, _, err := parseCandidatePostgresPolicy(content.RuntimePolicy.NetworkPolicy); err != nil {
		return CompiledCanonicalPlan{}, planInvalid("runtime PostgreSQL policy")
	}
	if services, err := parseCandidateRuntimeServices(content.RuntimePolicy.NetworkPolicy); err != nil {
		return CompiledCanonicalPlan{}, planInvalid("runtime services policy")
	} else if _, postgres, _ := parseCandidatePostgresPolicy(content.RuntimePolicy.NetworkPolicy); len(services) > 0 && !postgres {
		return CompiledCanonicalPlan{}, planInvalid("runtime services require PostgreSQL isolation")
	}
	runtimeJSON, err := domain.CanonicalJSON(content.RuntimePolicy)
	if err != nil || len(runtimeJSON) > 1<<20 {
		return CompiledCanonicalPlan{}, planInvalid("canonical runtime policy")
	}
	return CompiledCanonicalPlan{Content: content, PlanHash: expectedHash}, nil
}

func normalizeCanonicalPlanSubject(subject CanonicalPlanSubject) (CanonicalPlanSubject, error) {
	if !validUUIDs(subject.WorkspaceArtifactID, subject.WorkspaceRevisionID) || !exactSHA256(subject.WorkspaceContentHash) {
		return CanonicalPlanSubject{}, planInvalid("canonical WorkspaceRevision subject")
	}
	return subject, nil
}

func validateCanonicalReleaseChecks(checks []PlanCheck) error {
	required := map[string]string{
		"release-artifacts":        "release-manifest",
		"release-sbom":             "sbom",
		"release-vulnerability":    "vulnerability",
		"release-container-policy": "container-policy",
	}
	covered := map[string]bool{}
	for _, check := range checks {
		if kind, exists := required[check.ID]; exists && check.Required && check.Kind == kind {
			covered[check.ID] = true
		}
	}
	ids := make([]string, 0, len(required))
	for id := range required {
		if !covered[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	if len(ids) > 0 {
		return planInvalid("canonical release checks are missing: " + ids[0])
	}
	return nil
}
