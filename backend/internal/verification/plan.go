package verification

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/worksflow/builder/backend/internal/domain"
	"github.com/worksflow/builder/backend/internal/repository"
	"github.com/worksflow/builder/backend/internal/templates"
)

const (
	PlanContentSchemaVersionV1 = "candidate-verification-plan-content/v1"
	PlanContentSchemaVersion   = "candidate-verification-plan-content/v2"
)

var ErrInvalidPlan = errors.New("invalid candidate verification plan")

type VerifierImage struct {
	Role  string `json:"role"`
	Image string `json:"image"`
}

type ProfileBuiltInCheck struct {
	ID                     string   `json:"id"`
	Kind                   string   `json:"kind"`
	ImageRole              string   `json:"imageRole"`
	Argv                   []string `json:"argv"`
	WorkingDirectory       string   `json:"workingDirectory"`
	Required               bool     `json:"required"`
	OracleIDs              []string `json:"oracleIds"`
	AcceptanceCriterionIDs []string `json:"acceptanceCriterionIds"`
	ObligationIDs          []string `json:"obligationIds"`
	DependsOn              []string `json:"dependsOn"`
	TimeoutSeconds         int      `json:"timeoutSeconds"`
}

type VerificationProfileDocument struct {
	SchemaVersion          string                `json:"schemaVersion"`
	ID                     string                `json:"id"`
	Version                uint64                `json:"version"`
	ProfileHash            string                `json:"profileHash"`
	SupportedTemplateRoles []string              `json:"supportedTemplateRoles"`
	VerifierImages         []VerifierImage       `json:"verifierImages"`
	CommandImageRoles      map[string]string     `json:"commandImageRoles"`
	BuiltInChecks          []ProfileBuiltInCheck `json:"builtInChecks"`
	Limits                 map[string]any        `json:"limits"`
	NetworkPolicy          map[string]any        `json:"networkPolicy"`
	HiddenTestBundle       json.RawMessage       `json:"hiddenTestBundle,omitempty"`
	State                  string                `json:"state"`
}

type CandidatePlanSubject struct {
	SessionID           string `json:"sessionId"`
	SessionVersion      uint64 `json:"sessionVersion"`
	CandidateID         string `json:"candidateId"`
	CandidateSnapshotID string `json:"candidateSnapshotId"`
	CandidateVersion    uint64 `json:"candidateVersion"`
	JournalSequence     uint64 `json:"journalSequence"`
	SessionEpoch        uint64 `json:"sessionEpoch"`
	WriterLeaseEpoch    uint64 `json:"writerLeaseEpoch"`
	TreeStore           string `json:"treeStore"`
	TreeOwnerID         string `json:"treeOwnerId"`
	TreeRef             string `json:"treeRef"`
	TreeContentHash     string `json:"treeContentHash"`
	TreeHash            string `json:"treeHash"`
}

type ResolvedTemplateRelease struct {
	Role        string                     `json:"role"`
	MountPath   string                     `json:"mountPath"`
	Release     repository.ExactReference  `json:"release"`
	SubjectHash string                     `json:"subjectHash"`
	Manifest    templates.TemplateManifest `json:"-"`
}

type PlanTemplateRelease struct {
	Role        string                    `json:"role"`
	MountPath   string                    `json:"mountPath"`
	Release     repository.ExactReference `json:"release"`
	SubjectHash string                    `json:"subjectHash"`
}

type PlanDependencyLock struct {
	Path     string `json:"path"`
	Digest   string `json:"digest"`
	Registry string `json:"registry"`
}

// PlanDependency is compiled exclusively from an approved TemplateRelease
// manifest and the active VerificationProfile. Browser requests cannot add a
// resolver image, command, registry, input path, or cache identity.
type PlanDependency struct {
	ID                   string               `json:"id"`
	ServiceID            string               `json:"serviceId"`
	Ecosystem            string               `json:"ecosystem"`
	WorkingDirectory     string               `json:"workingDirectory"`
	ToolchainImageDigest string               `json:"toolchainImageDigest"`
	ManifestPaths        []string             `json:"manifestPaths"`
	Lockfiles            []PlanDependencyLock `json:"lockfiles"`
	ResolverArgv         []string             `json:"resolverArgv"`
	CacheKey             string               `json:"cacheKey"`
}

type PlanOracle struct {
	ID                     string   `json:"id"`
	Kind                   string   `json:"kind"`
	Target                 string   `json:"target"`
	CommandID              string   `json:"commandId"`
	AcceptanceCriterionIDs []string `json:"acceptanceCriterionIds"`
}

type PlanObligation struct {
	ID        string   `json:"id"`
	Level     string   `json:"level"`
	Status    string   `json:"status"`
	OracleIDs []string `json:"oracleIds"`
}

type PlanCheck struct {
	ID                     string   `json:"id"`
	Kind                   string   `json:"kind"`
	ServiceID              string   `json:"serviceId,omitempty"`
	CommandID              string   `json:"commandId,omitempty"`
	Required               bool     `json:"required"`
	VerifierImageDigest    string   `json:"verifierImageDigest"`
	Argv                   []string `json:"argv"`
	WorkingDirectory       string   `json:"workingDirectory"`
	OracleIDs              []string `json:"oracleIds"`
	AcceptanceCriterionIDs []string `json:"acceptanceCriterionIds"`
	ObligationIDs          []string `json:"obligationIds"`
	DependsOn              []string `json:"dependsOn"`
	TimeoutSeconds         int      `json:"timeoutSeconds"`
}

type PlanRuntimePolicy struct {
	Limits           map[string]any  `json:"limits"`
	NetworkPolicy    map[string]any  `json:"networkPolicy"`
	HiddenTestBundle json.RawMessage `json:"hiddenTestBundle,omitempty"`
}

type PlanContent struct {
	SchemaVersion     string                    `json:"schemaVersion"`
	Scope             Scope                     `json:"scope"`
	ProjectID         string                    `json:"projectId"`
	Subject           CandidatePlanSubject      `json:"subject"`
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

type CompileCandidatePlanInput struct {
	ProjectID         string
	Subject           CandidatePlanSubject
	BuildManifest     repository.ExactReference
	BuildContract     repository.ExactReference
	FullStackTemplate repository.ExactReference
	Profile           VerificationProfileDocument
	TemplateReleases  []ResolvedTemplateRelease
	Oracles           []PlanOracle
	Obligations       []PlanObligation
}

type CompiledPlan struct {
	Content  PlanContent `json:"content"`
	PlanHash string      `json:"planHash"`
}

type PlanCompiler struct{}

func (PlanCompiler) Compile(input CompileCandidatePlanInput) (CompiledPlan, error) {
	profile, images, err := normalizeVerificationProfile(input.Profile)
	if err != nil {
		return CompiledPlan{}, err
	}
	subject, err := normalizePlanSubject(input.Subject)
	if err != nil {
		return CompiledPlan{}, err
	}
	if !validUUIDs(input.ProjectID) {
		return CompiledPlan{}, planInvalid("project identity")
	}
	manifest, err := normalizeExactReference(input.BuildManifest, "build manifest")
	if err != nil {
		return CompiledPlan{}, planInvalid(err.Error())
	}
	contract, err := normalizeExactReference(input.BuildContract, "build contract")
	if err != nil {
		return CompiledPlan{}, planInvalid(err.Error())
	}
	fullStack, err := normalizeExactReference(input.FullStackTemplate, "full-stack template")
	if err != nil {
		return CompiledPlan{}, planInvalid(err.Error())
	}
	releases, resolved, err := normalizeResolvedReleases(input.TemplateReleases, profile)
	if err != nil {
		return CompiledPlan{}, err
	}
	dependencies, err := compilePlanDependencies(resolved, profile)
	if err != nil {
		return CompiledPlan{}, err
	}
	if len(dependencies) > 0 {
		if _, err := parseCandidateDependencyResolverPolicy(profile.NetworkPolicy); err != nil {
			return CompiledPlan{}, planInvalid("dependency resolver network policy")
		}
	}
	oracles, err := normalizePlanOracles(input.Oracles)
	if err != nil {
		return CompiledPlan{}, err
	}
	obligations, requiredOracleIDs, oracleObligations, err := normalizePlanObligations(input.Obligations, oracles)
	if err != nil {
		return CompiledPlan{}, err
	}
	checks, err := compileOracleChecks(
		oracles, obligations, requiredOracleIDs, oracleObligations, resolved, profile, images,
	)
	if err != nil {
		return CompiledPlan{}, err
	}
	checks, err = appendBuiltInChecks(
		checks, profile.BuiltInChecks, oracles, requiredOracleIDs, oracleObligations, images,
	)
	if err != nil {
		return CompiledPlan{}, err
	}
	if err := validateCheckDependencies(checks); err != nil {
		return CompiledPlan{}, err
	}
	content := PlanContent{
		SchemaVersion: PlanContentSchemaVersion, Scope: ScopeCandidate,
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
		return CompiledPlan{}, planInvalid("canonical content")
	}
	return CompiledPlan{Content: content, PlanHash: "sha256:" + hash}, nil
}

func ParsePlan(content PlanContent, expectedHash string) (CompiledPlan, error) {
	hash, err := domain.CanonicalHash(content)
	if err != nil || expectedHash != "sha256:"+hash {
		return CompiledPlan{}, planInvalid("content hash")
	}
	if err := validatePersistedPlanContent(content); err != nil {
		return CompiledPlan{}, err
	}
	return CompiledPlan{Content: content, PlanHash: expectedHash}, nil
}

func validatePersistedPlanContent(content PlanContent) error {
	if !supportedPlanContentSchema(content.SchemaVersion) || content.Scope != ScopeCandidate ||
		!validUUIDs(content.ProjectID) {
		return planInvalid("envelope")
	}
	subject, err := normalizePlanSubject(content.Subject)
	if err != nil || subject != content.Subject {
		return planInvalid("Candidate subject")
	}
	for field, reference := range map[string]repository.ExactReference{
		"build manifest": content.BuildManifest, "build contract": content.BuildContract,
		"full-stack template": content.FullStackTemplate,
	} {
		normalized, err := normalizeExactReference(reference, field)
		if err != nil || normalized != reference {
			return planInvalid(field)
		}
	}
	profile, err := normalizeProfile(content.Profile)
	if err != nil || profile != content.Profile {
		return planInvalid("verification profile")
	}
	if err := validatePersistedPlanTemplates(content.TemplateReleases); err != nil {
		return err
	}
	if content.SchemaVersion == PlanContentSchemaVersion {
		if err := validatePersistedPlanDependencies(content.Dependencies, content.TemplateReleases, content.Profile); err != nil {
			return err
		}
		if _, err := parseCandidateDependencyResolverPolicy(content.RuntimePolicy.NetworkPolicy); err != nil {
			return planInvalid("dependency resolver network policy")
		}
	} else if len(content.Dependencies) != 0 {
		return planInvalid("legacy Plan cannot contain dependency projections")
	}
	obligationOracles, mustOracles, err := validatePersistedPlanObligations(content.Obligations)
	if err != nil {
		return err
	}
	if err := validatePersistedPlanChecks(content.Checks, obligationOracles, mustOracles); err != nil {
		return err
	}
	if content.RuntimePolicy.Limits == nil || content.RuntimePolicy.NetworkPolicy == nil {
		return planInvalid("runtime policy must use stable objects")
	}
	if _, _, err := parseCandidatePostgresPolicy(content.RuntimePolicy.NetworkPolicy); err != nil {
		return planInvalid("runtime PostgreSQL policy")
	}
	if services, err := parseCandidateRuntimeServices(content.RuntimePolicy.NetworkPolicy); err != nil {
		return planInvalid("runtime services policy")
	} else if _, postgres, _ := parseCandidatePostgresPolicy(content.RuntimePolicy.NetworkPolicy); len(services) > 0 && !postgres {
		return planInvalid("runtime services require PostgreSQL isolation")
	}
	runtimeJSON, err := domain.CanonicalJSON(content.RuntimePolicy)
	if err != nil || len(runtimeJSON) > 1<<20 {
		return planInvalid("runtime policy")
	}
	return nil
}

func supportedPlanContentSchema(value string) bool {
	return value == PlanContentSchemaVersionV1 || value == PlanContentSchemaVersion
}

func validatePersistedPlanTemplates(values []PlanTemplateRelease) error {
	if len(values) < 2 || len(values) > 8 {
		return planInvalid("TemplateRelease count")
	}
	seenRoles, seenIDs, seenMounts := map[string]bool{}, map[string]bool{}, map[string]bool{}
	previousRole := ""
	for index, value := range values {
		mount, err := repository.NormalizePath(value.MountPath)
		if err != nil || mount != value.MountPath ||
			(value.Role != "web" && value.Role != "api" && value.Role != "worker") ||
			seenRoles[value.Role] || seenIDs[value.Release.ID] || seenMounts[value.MountPath] ||
			(previousRole != "" && value.Role <= previousRole) || !validUUIDs(value.Release.ID) ||
			!exactSHA256(value.Release.ContentHash) || !exactSHA256(value.SubjectHash) {
			return planInvalid(fmt.Sprintf("TemplateRelease projection %d", index))
		}
		seenRoles[value.Role], seenIDs[value.Release.ID], seenMounts[value.MountPath] = true, true, true
		previousRole = value.Role
	}
	if !seenRoles["web"] || !seenRoles["api"] {
		return planInvalid("web and api TemplateReleases are required")
	}
	return nil
}

func validatePersistedPlanObligations(
	values []PlanObligation,
) (map[string]map[string]bool, map[string]bool, error) {
	if len(values) == 0 || len(values) > 1024 {
		return nil, nil, planInvalid("obligations")
	}
	obligationOracles := make(map[string]map[string]bool, len(values))
	mustOracles := map[string]bool{}
	previousID, mustCount := "", 0
	for index, value := range values {
		minimum := 1
		if value.Status == "waived" {
			minimum = 0
		}
		oracles, err := normalizeStableList(value.OracleIDs, minimum, 256, "obligation Oracle IDs")
		if err != nil || !equalStrings(oracles, value.OracleIDs) || !stableIDPattern.MatchString(value.ID) ||
			(previousID != "" && value.ID <= previousID) ||
			(value.Level != "must" && value.Level != "should") ||
			(value.Status != "ready" && value.Status != "waived") ||
			(value.Level == "must" && value.Status != "ready") {
			return nil, nil, planInvalid(fmt.Sprintf("obligation projection %d", index))
		}
		set := stringSet(oracles)
		obligationOracles[value.ID] = set
		if value.Level == "must" {
			mustCount++
			for _, oracleID := range oracles {
				mustOracles[value.ID+"\x00"+oracleID] = true
			}
		}
		previousID = value.ID
	}
	if mustCount == 0 {
		return nil, nil, planInvalid("at least one Must obligation is required")
	}
	return obligationOracles, mustOracles, nil
}

func validatePersistedPlanChecks(
	values []PlanCheck,
	obligationOracles map[string]map[string]bool,
	mustOracles map[string]bool,
) error {
	if len(values) == 0 || len(values) > 512 {
		return planInvalid("checks")
	}
	knownOracles := map[string]bool{}
	for _, oracles := range obligationOracles {
		for oracleID := range oracles {
			knownOracles[oracleID] = true
		}
	}
	coveredMustOracles := map[string]bool{}
	previousID, requiredCount := "", 0
	for index, value := range values {
		if !stableIDPattern.MatchString(value.ID) || !stableIDPattern.MatchString(value.Kind) ||
			(previousID != "" && value.ID <= previousID) || !imagePattern.MatchString(value.VerifierImageDigest) ||
			len(value.Argv) == 0 || len(value.Argv) > 64 || value.TimeoutSeconds < 1 || value.TimeoutSeconds > 7200 ||
			(value.WorkingDirectory != "." && !validRelativePath(value.WorkingDirectory)) ||
			(value.ServiceID != "" && !stableIDPattern.MatchString(value.ServiceID)) ||
			(value.CommandID != "" && !stableIDPattern.MatchString(value.CommandID)) {
			return planInvalid(fmt.Sprintf("check projection %d", index))
		}
		for _, argument := range value.Argv {
			if argument == "" || len(argument) > 4096 || strings.ContainsRune(argument, '\x00') {
				return planInvalid(fmt.Sprintf("check projection %d argv", index))
			}
		}
		oracleIDs, oracleErr := normalizeStableList(value.OracleIDs, 0, 256, "check Oracle IDs")
		acceptanceIDs, acceptanceErr := normalizeStableList(value.AcceptanceCriterionIDs, 0, 256, "check acceptance IDs")
		obligationIDs, obligationErr := normalizeStableList(value.ObligationIDs, 0, 256, "check obligation IDs")
		dependencies, dependencyErr := normalizeStableList(value.DependsOn, 0, 64, "check dependencies")
		if oracleErr != nil || acceptanceErr != nil || obligationErr != nil || dependencyErr != nil ||
			!equalStrings(oracleIDs, value.OracleIDs) || !equalStrings(acceptanceIDs, value.AcceptanceCriterionIDs) ||
			!equalStrings(obligationIDs, value.ObligationIDs) || !equalStrings(dependencies, value.DependsOn) ||
			(len(oracleIDs) > 0 && len(acceptanceIDs) == 0) {
			return planInvalid(fmt.Sprintf("check projection %d bindings", index))
		}
		for _, oracleID := range oracleIDs {
			if !knownOracles[oracleID] {
				return planInvalid("check references unknown Oracle " + oracleID)
			}
		}
		for _, obligationID := range obligationIDs {
			oracles, exists := obligationOracles[obligationID]
			if !exists || !intersects(oracleIDs, oracles) {
				return planInvalid("check coverage does not match obligation " + obligationID)
			}
			if value.Required {
				for _, oracleID := range oracleIDs {
					if oracles[oracleID] {
						coveredMustOracles[obligationID+"\x00"+oracleID] = true
					}
				}
			}
		}
		if value.Required {
			requiredCount++
		}
		previousID = value.ID
	}
	if requiredCount == 0 {
		return planInvalid("Plan has no required checks")
	}
	for key := range mustOracles {
		if !coveredMustOracles[key] {
			return planInvalid("Must Oracle has no required exact check")
		}
	}
	return validateCheckDependencies(values)
}

func normalizeVerificationProfile(
	profile VerificationProfileDocument,
) (VerificationProfileDocument, map[string]string, error) {
	profile.ID = strings.TrimSpace(profile.ID)
	if profile.SchemaVersion != "verification-profile/v1" || !stableIDPattern.MatchString(profile.ID) ||
		profile.Version == 0 || !exactSHA256(profile.ProfileHash) || profile.State != "active" ||
		len(profile.VerifierImages) == 0 || len(profile.VerifierImages) > 16 ||
		len(profile.SupportedTemplateRoles) == 0 || len(profile.CommandImageRoles) == 0 {
		return VerificationProfileDocument{}, nil, planInvalid("verification profile")
	}
	roles, err := normalizeStableList(profile.SupportedTemplateRoles, 1, 8, "supported roles")
	if err != nil {
		return VerificationProfileDocument{}, nil, planInvalid("supported roles")
	}
	profile.SupportedTemplateRoles = roles
	images := make(map[string]string, len(profile.VerifierImages))
	for index := range profile.VerifierImages {
		role := strings.TrimSpace(profile.VerifierImages[index].Role)
		image := strings.TrimSpace(profile.VerifierImages[index].Image)
		if !stableIDPattern.MatchString(role) || !imagePattern.MatchString(image) || images[role] != "" {
			return VerificationProfileDocument{}, nil, planInvalid("verifier images")
		}
		images[role] = image
		profile.VerifierImages[index] = VerifierImage{Role: role, Image: image}
	}
	sort.Slice(profile.VerifierImages, func(left, right int) bool {
		return profile.VerifierImages[left].Role < profile.VerifierImages[right].Role
	})
	commandRoles := make(map[string]string, len(profile.CommandImageRoles))
	for role, imageRole := range profile.CommandImageRoles {
		role, imageRole = strings.TrimSpace(role), strings.TrimSpace(imageRole)
		if !stableIDPattern.MatchString(role) || images[imageRole] == "" || commandRoles[role] != "" {
			return VerificationProfileDocument{}, nil, planInvalid("command image roles")
		}
		commandRoles[role] = imageRole
	}
	profile.CommandImageRoles = commandRoles
	if profile.BuiltInChecks == nil {
		profile.BuiltInChecks = []ProfileBuiltInCheck{}
	}
	if profile.Limits == nil {
		profile.Limits = map[string]any{}
	}
	if profile.NetworkPolicy == nil {
		profile.NetworkPolicy = map[string]any{}
	}
	if _, _, err := parseCandidatePostgresPolicy(profile.NetworkPolicy); err != nil {
		return VerificationProfileDocument{}, nil, planInvalid("runtime PostgreSQL policy")
	}
	if services, err := parseCandidateRuntimeServices(profile.NetworkPolicy); err != nil {
		return VerificationProfileDocument{}, nil, planInvalid("runtime services policy")
	} else if _, postgres, _ := parseCandidatePostgresPolicy(profile.NetworkPolicy); len(services) > 0 && !postgres {
		return VerificationProfileDocument{}, nil, planInvalid("runtime services require PostgreSQL isolation")
	}
	return profile, images, nil
}

func normalizePlanSubject(subject CandidatePlanSubject) (CandidatePlanSubject, error) {
	if !validUUIDs(subject.SessionID, subject.CandidateID, subject.CandidateSnapshotID, subject.TreeOwnerID) ||
		subject.SessionVersion == 0 || subject.CandidateVersion == 0 || subject.SessionEpoch == 0 ||
		subject.WriterLeaseEpoch == 0 || strings.TrimSpace(subject.TreeStore) == "" ||
		strings.TrimSpace(subject.TreeRef) == "" || !exactSHA256(subject.TreeContentHash) ||
		!exactSHA256(subject.TreeHash) {
		return CandidatePlanSubject{}, planInvalid("Candidate subject")
	}
	subject.TreeStore = strings.TrimSpace(subject.TreeStore)
	subject.TreeRef = strings.TrimSpace(subject.TreeRef)
	return subject, nil
}

func normalizeResolvedReleases(
	values []ResolvedTemplateRelease,
	profile VerificationProfileDocument,
) ([]PlanTemplateRelease, []ResolvedTemplateRelease, error) {
	if len(values) < 2 || len(values) > 8 {
		return nil, nil, planInvalid("TemplateRelease count")
	}
	supported := stringSet(profile.SupportedTemplateRoles)
	seenRoles, seenReleases, seenMounts := map[string]bool{}, map[string]bool{}, map[string]bool{}
	result := make([]PlanTemplateRelease, 0, len(values))
	resolved := make([]ResolvedTemplateRelease, 0, len(values))
	for index := range values {
		value := values[index]
		value.Role = strings.TrimSpace(value.Role)
		mount, err := repository.NormalizePath(value.MountPath)
		if err != nil || !supported[value.Role] || profile.CommandImageRoles[value.Role] == "" ||
			seenRoles[value.Role] || seenMounts[mount] || seenReleases[value.Release.ID] ||
			!validUUIDs(value.Release.ID) || !exactSHA256(value.Release.ContentHash) ||
			!exactSHA256(value.SubjectHash) || value.Manifest.SchemaVersion != templates.TemplateManifestSchemaVersion {
			return nil, nil, planInvalid(fmt.Sprintf("TemplateRelease %d", index))
		}
		value.MountPath = mount
		seenRoles[value.Role], seenMounts[mount], seenReleases[value.Release.ID] = true, true, true
		resolved = append(resolved, value)
		result = append(result, PlanTemplateRelease{
			Role: value.Role, MountPath: mount, Release: value.Release, SubjectHash: value.SubjectHash,
		})
	}
	if !seenRoles["web"] || !seenRoles["api"] {
		return nil, nil, planInvalid("web and api TemplateReleases are required")
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Role < result[right].Role })
	sort.Slice(resolved, func(left, right int) bool { return resolved[left].Role < resolved[right].Role })
	return result, resolved, nil
}

func compilePlanDependencies(
	releases []ResolvedTemplateRelease,
	profile VerificationProfileDocument,
) ([]PlanDependency, error) {
	result := make([]PlanDependency, 0, len(releases))
	for _, release := range releases {
		ecosystem := ""
		switch release.Role {
		case "web":
			ecosystem = "node"
		case "api":
			ecosystem = "python"
		case "worker":
			for _, toolchain := range release.Manifest.Toolchains {
				if toolchain.Name == "node" || toolchain.Name == "python" {
					if ecosystem != "" {
						return nil, planInvalid("worker TemplateRelease has ambiguous dependency ecosystem")
					}
					ecosystem = toolchain.Name
				}
			}
		}
		if ecosystem == "" {
			return nil, planInvalid("TemplateRelease dependency ecosystem: " + release.Role)
		}
		toolchainImage := ""
		for _, toolchain := range release.Manifest.Toolchains {
			if toolchain.Name != ecosystem {
				continue
			}
			if toolchainImage != "" || !imagePattern.MatchString(toolchain.Image) {
				return nil, planInvalid("TemplateRelease toolchain image: " + release.Role)
			}
			toolchainImage = toolchain.Image
		}
		if toolchainImage == "" {
			return nil, planInvalid("TemplateRelease is missing the exact " + ecosystem + " toolchain: " + release.Role)
		}

		var selected *templates.Lockfile
		for index := range release.Manifest.Lockfiles {
			lockfile := release.Manifest.Lockfiles[index]
			base := path.Base(lockfile.Path)
			matches := ecosystem == "node" && base == "package-lock.json" ||
				ecosystem == "python" && (base == "requirements.lock" || base == "requirements.txt")
			if !matches {
				continue
			}
			if selected != nil {
				return nil, planInvalid("TemplateRelease has ambiguous dependency lockfiles: " + release.Role)
			}
			copy := lockfile
			selected = &copy
		}
		if selected == nil || !exactSHA256(selected.Digest) || !validDependencyRegistry(selected.Registry) {
			return nil, planInvalid("TemplateRelease dependency lockfile: " + release.Role)
		}
		lockPath, err := planWorkingDirectory(release.MountPath, selected.Path)
		if err != nil || lockPath == "." {
			return nil, planInvalid("TemplateRelease dependency lockfile path: " + release.Role)
		}
		workingDirectory := path.Dir(lockPath)
		if workingDirectory == "" {
			workingDirectory = "."
		}
		manifestPaths := []string{}
		resolverArgv := []string{}
		switch ecosystem {
		case "node":
			manifestPath := path.Join(workingDirectory, "package.json")
			if workingDirectory == "." {
				manifestPath = "package.json"
			}
			manifestPaths = []string{manifestPath}
			resolverArgv = []string{
				"npm", "ci", "--ignore-scripts", "--no-audit", "--no-fund",
				"--registry=" + selected.Registry,
			}
		case "python":
			resolverArgv = []string{
				"python", "-m", "pip", "install", "--require-hashes", "--only-binary=:all:",
				"--target", "/resolver/site-packages", "--requirement", "/resolver/" + path.Base(lockPath),
				"--index-url", selected.Registry, "--disable-pip-version-check", "--no-input",
			}
		}
		dependency := PlanDependency{
			ID: "dependency-" + release.Role, ServiceID: release.Role, Ecosystem: ecosystem,
			WorkingDirectory: workingDirectory, ToolchainImageDigest: toolchainImage,
			ManifestPaths: manifestPaths,
			Lockfiles: []PlanDependencyLock{{
				Path: lockPath, Digest: selected.Digest, Registry: selected.Registry,
			}},
			ResolverArgv: resolverArgv,
		}
		key, err := domain.CanonicalHash(struct {
			Dependency  PlanDependency `json:"dependency"`
			ProfileHash string         `json:"profileHash"`
		}{Dependency: dependency, ProfileHash: profile.ProfileHash})
		if err != nil {
			return nil, planInvalid("dependency cache identity")
		}
		dependency.CacheKey = "sha256:" + key
		result = append(result, dependency)
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result, nil
}

func validatePersistedPlanDependencies(
	values []PlanDependency,
	releases []PlanTemplateRelease,
	profile ProfileReference,
) error {
	if len(values) != len(releases) || len(values) < 2 || len(values) > 8 {
		return planInvalid("dependency projection count")
	}
	releaseRoles := map[string]bool{}
	for _, release := range releases {
		releaseRoles[release.Role] = true
	}
	previousID := ""
	for index, value := range values {
		if !stableIDPattern.MatchString(value.ID) || value.ID != "dependency-"+value.ServiceID ||
			!releaseRoles[value.ServiceID] || (previousID != "" && value.ID <= previousID) ||
			(value.Ecosystem != "node" && value.Ecosystem != "python") ||
			!imagePattern.MatchString(value.ToolchainImageDigest) || !exactSHA256(value.CacheKey) ||
			(value.WorkingDirectory != "." && !validRelativePath(value.WorkingDirectory)) ||
			len(value.Lockfiles) != 1 || len(value.ResolverArgv) == 0 || len(value.ResolverArgv) > 32 {
			return planInvalid(fmt.Sprintf("dependency projection %d", index))
		}
		manifests, err := normalizeStableList(value.ManifestPaths, 0, 8, "dependency manifest paths")
		if err != nil || !equalStrings(manifests, value.ManifestPaths) {
			return planInvalid(fmt.Sprintf("dependency projection %d manifests", index))
		}
		for _, manifest := range manifests {
			if !validRelativePath(manifest) {
				return planInvalid(fmt.Sprintf("dependency projection %d manifest path", index))
			}
		}
		lockfile := value.Lockfiles[0]
		if !validRelativePath(lockfile.Path) || !exactSHA256(lockfile.Digest) ||
			!validDependencyRegistry(lockfile.Registry) ||
			(value.Ecosystem == "node" && path.Base(lockfile.Path) != "package-lock.json") ||
			(value.Ecosystem == "python" && path.Base(lockfile.Path) != "requirements.lock" && path.Base(lockfile.Path) != "requirements.txt") {
			return planInvalid(fmt.Sprintf("dependency projection %d lockfile", index))
		}
		for _, argument := range value.ResolverArgv {
			if argument == "" || len(argument) > 4096 || strings.ContainsRune(argument, '\x00') {
				return planInvalid(fmt.Sprintf("dependency projection %d argv", index))
			}
		}
		expectedArgv := expectedDependencyResolverArgv(value)
		if !equalStrings(expectedArgv, value.ResolverArgv) {
			return planInvalid(fmt.Sprintf("dependency projection %d resolver command", index))
		}
		withoutKey := value
		withoutKey.CacheKey = ""
		key, err := domain.CanonicalHash(struct {
			Dependency  PlanDependency `json:"dependency"`
			ProfileHash string         `json:"profileHash"`
		}{Dependency: withoutKey, ProfileHash: profile.ContentHash})
		if err != nil || value.CacheKey != "sha256:"+key {
			return planInvalid(fmt.Sprintf("dependency projection %d cache identity", index))
		}
		previousID = value.ID
	}
	return nil
}

func expectedDependencyResolverArgv(value PlanDependency) []string {
	lockfile := value.Lockfiles[0]
	if value.Ecosystem == "node" {
		return []string{
			"npm", "ci", "--ignore-scripts", "--no-audit", "--no-fund",
			"--registry=" + lockfile.Registry,
		}
	}
	return []string{
		"python", "-m", "pip", "install", "--require-hashes", "--only-binary=:all:",
		"--target", "/resolver/site-packages", "--requirement", "/resolver/" + path.Base(lockfile.Path),
		"--index-url", lockfile.Registry, "--disable-pip-version-check", "--no-input",
	}
}

func validDependencyRegistry(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil &&
		parsed.RawQuery == "" && parsed.Fragment == "" && strings.TrimSpace(value) == value
}

func normalizePlanOracles(values []PlanOracle) (map[string]PlanOracle, error) {
	if len(values) == 0 || len(values) > 1024 {
		return nil, planInvalid("oracles")
	}
	result := make(map[string]PlanOracle, len(values))
	for index := range values {
		value := values[index]
		value.ID, value.Kind = strings.TrimSpace(value.ID), strings.TrimSpace(value.Kind)
		value.Target, value.CommandID = strings.TrimSpace(value.Target), strings.TrimSpace(value.CommandID)
		acceptance, err := normalizeStableList(value.AcceptanceCriterionIDs, 1, 256, "Oracle acceptance IDs")
		if err != nil || !stableIDPattern.MatchString(value.ID) || !stableIDPattern.MatchString(value.Kind) ||
			value.Target == "" || result[value.ID].ID != "" {
			return nil, planInvalid(fmt.Sprintf("Oracle %d", index))
		}
		value.AcceptanceCriterionIDs = acceptance
		result[value.ID] = value
	}
	return result, nil
}

func normalizePlanObligations(
	values []PlanObligation,
	oracles map[string]PlanOracle,
) ([]PlanObligation, map[string]bool, map[string][]string, error) {
	if len(values) == 0 || len(values) > 1024 {
		return nil, nil, nil, planInvalid("obligations")
	}
	result := make([]PlanObligation, len(values))
	requiredOracles, oracleObligations, seen := map[string]bool{}, map[string][]string{}, map[string]bool{}
	mustCount := 0
	for index := range values {
		value := values[index]
		value.ID = strings.TrimSpace(value.ID)
		oracleIDs, err := normalizeStableList(value.OracleIDs, 1, 256, "obligation Oracle IDs")
		if err != nil || !stableIDPattern.MatchString(value.ID) || seen[value.ID] ||
			(value.Level != "must" && value.Level != "should") || value.Status != "ready" {
			return nil, nil, nil, planInvalid(fmt.Sprintf("obligation %d", index))
		}
		seen[value.ID] = true
		value.OracleIDs = oracleIDs
		if value.Level == "must" {
			mustCount++
		}
		for _, oracleID := range oracleIDs {
			if oracles[oracleID].ID == "" {
				return nil, nil, nil, planInvalid("unknown obligation Oracle " + oracleID)
			}
			oracleObligations[oracleID] = append(oracleObligations[oracleID], value.ID)
			if value.Level == "must" {
				requiredOracles[oracleID] = true
			}
		}
		result[index] = value
	}
	if mustCount == 0 {
		return nil, nil, nil, planInvalid("at least one Must obligation is required")
	}
	for oracleID := range oracleObligations {
		sort.Strings(oracleObligations[oracleID])
	}
	sort.Slice(result, func(left, right int) bool { return result[left].ID < result[right].ID })
	return result, requiredOracles, oracleObligations, nil
}

func compileOracleChecks(
	oracles map[string]PlanOracle,
	_ []PlanObligation,
	requiredOracles map[string]bool,
	oracleObligations map[string][]string,
	releases []ResolvedTemplateRelease,
	profile VerificationProfileDocument,
	images map[string]string,
) ([]PlanCheck, error) {
	result := []PlanCheck{}
	oracleIDs := make([]string, 0, len(oracleObligations))
	for oracleID := range oracleObligations {
		oracleIDs = append(oracleIDs, oracleID)
	}
	sort.Strings(oracleIDs)
	for _, oracleID := range oracleIDs {
		oracle := oracles[oracleID]
		if oracle.CommandID == "" {
			continue
		}
		matches := []struct {
			release ResolvedTemplateRelease
			command templates.Command
		}{}
		for _, release := range releases {
			if command, ok := release.Manifest.Commands[oracle.CommandID]; ok {
				matches = append(matches, struct {
					release ResolvedTemplateRelease
					command templates.Command
				}{release: release, command: command})
			}
		}
		if len(matches) != 1 {
			return nil, planInvalid("Oracle command must resolve to one exact TemplateRelease: " + oracle.ID)
		}
		match := matches[0]
		workingDirectory, err := planWorkingDirectory(match.release.MountPath, match.command.WorkingDirectory)
		if err != nil || len(match.command.Argv) == 0 || len(match.command.Argv) > 64 {
			return nil, planInvalid("invalid Oracle command: " + oracle.ID)
		}
		for _, argument := range match.command.Argv {
			if argument == "" || len(argument) > 4096 || strings.ContainsRune(argument, '\x00') {
				return nil, planInvalid("invalid Oracle argv: " + oracle.ID)
			}
		}
		image := images[profile.CommandImageRoles[match.release.Role]]
		if image == "" {
			return nil, planInvalid("missing verifier image for TemplateRelease role " + match.release.Role)
		}
		result = append(result, PlanCheck{
			ID: oracle.ID, Kind: oracle.Kind, ServiceID: match.release.Role,
			CommandID: oracle.CommandID, Required: requiredOracles[oracle.ID],
			VerifierImageDigest: image, Argv: append([]string{}, match.command.Argv...),
			WorkingDirectory: workingDirectory, OracleIDs: []string{oracle.ID},
			AcceptanceCriterionIDs: append([]string{}, oracle.AcceptanceCriterionIDs...),
			ObligationIDs:          append([]string{}, oracleObligations[oracle.ID]...),
			DependsOn:              []string{}, TimeoutSeconds: profileCheckTimeout(profile),
		})
	}
	return result, nil
}

func appendBuiltInChecks(
	checks []PlanCheck,
	builtins []ProfileBuiltInCheck,
	oracleDefinitions map[string]PlanOracle,
	requiredOracles map[string]bool,
	oracleObligations map[string][]string,
	images map[string]string,
) ([]PlanCheck, error) {
	seenChecks, resolvedOracles := map[string]bool{}, map[string]bool{}
	for _, check := range checks {
		seenChecks[check.ID] = true
		for _, oracleID := range check.OracleIDs {
			resolvedOracles[oracleID] = true
		}
	}
	for index := range builtins {
		value := builtins[index]
		value.ID, value.Kind, value.ImageRole = strings.TrimSpace(value.ID), strings.TrimSpace(value.Kind), strings.TrimSpace(value.ImageRole)
		oracleIDs, err := normalizeStableList(value.OracleIDs, 0, 256, "built-in Oracle IDs")
		if err != nil || !stableIDPattern.MatchString(value.ID) || !stableIDPattern.MatchString(value.Kind) ||
			seenChecks[value.ID] || images[value.ImageRole] == "" || len(value.Argv) == 0 ||
			value.TimeoutSeconds < 1 || value.TimeoutSeconds > 7200 {
			return nil, planInvalid(fmt.Sprintf("built-in check %d", index))
		}
		workingDirectory := strings.TrimSpace(value.WorkingDirectory)
		if workingDirectory != "." {
			workingDirectory, err = repository.NormalizePath(workingDirectory)
			if err != nil {
				return nil, planInvalid("built-in working directory")
			}
		}
		required := value.Required
		acceptanceSet, obligationSet := map[string]bool{}, map[string]bool{}
		for _, oracleID := range oracleIDs {
			oracle, exists := oracleDefinitions[oracleID]
			if !exists {
				return nil, planInvalid("built-in check references unknown Oracle " + oracleID)
			}
			resolvedOracles[oracleID] = true
			required = required || requiredOracles[oracleID]
			for _, acceptanceID := range oracle.AcceptanceCriterionIDs {
				acceptanceSet[acceptanceID] = true
			}
			for _, obligationID := range oracleObligations[oracleID] {
				obligationSet[obligationID] = true
			}
		}
		acceptance, err := normalizeStableList(value.AcceptanceCriterionIDs, 0, 256, "built-in acceptance IDs")
		if err != nil {
			return nil, planInvalid("built-in acceptance IDs")
		}
		obligations, err := normalizeStableList(value.ObligationIDs, 0, 256, "built-in obligation IDs")
		if err != nil {
			return nil, planInvalid("built-in obligation IDs")
		}
		dependsOn, err := normalizeStableList(value.DependsOn, 0, 64, "built-in dependencies")
		if err != nil {
			return nil, planInvalid("built-in dependencies")
		}
		if len(oracleIDs) == 0 && (len(acceptance) != 0 || len(obligations) != 0) {
			return nil, planInvalid("built-in check cannot claim acceptance or obligation coverage without an Oracle")
		}
		expectedAcceptance, expectedObligations := sortedSet(acceptanceSet), sortedSet(obligationSet)
		if len(oracleIDs) > 0 && (!equalStrings(acceptance, expectedAcceptance) ||
			!equalStrings(obligations, expectedObligations)) {
			return nil, planInvalid("built-in check coverage differs from authoritative Oracle bindings")
		}
		if len(value.Argv) > 64 {
			return nil, planInvalid("built-in check argv")
		}
		for _, argument := range value.Argv {
			if argument == "" || len(argument) > 4096 || strings.ContainsRune(argument, '\x00') {
				return nil, planInvalid("built-in check argv")
			}
		}
		checks = append(checks, PlanCheck{
			ID: value.ID, Kind: value.Kind, Required: required,
			VerifierImageDigest: images[value.ImageRole], Argv: append([]string{}, value.Argv...),
			WorkingDirectory: workingDirectory, OracleIDs: oracleIDs,
			AcceptanceCriterionIDs: acceptance, ObligationIDs: obligations,
			DependsOn: dependsOn, TimeoutSeconds: value.TimeoutSeconds,
		})
		seenChecks[value.ID] = true
	}
	for oracleID := range requiredOracles {
		if !resolvedOracles[oracleID] {
			return nil, planInvalid("Must Oracle has no executable check: " + oracleID)
		}
	}
	sort.Slice(checks, func(left, right int) bool { return checks[left].ID < checks[right].ID })
	return checks, nil
}

func sortedSet(values map[string]bool) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func validateCheckDependencies(checks []PlanCheck) error {
	checkSet := map[string]bool{}
	for _, check := range checks {
		if checkSet[check.ID] {
			return planInvalid("duplicate check ID")
		}
		checkSet[check.ID] = true
	}
	for _, check := range checks {
		for _, dependency := range check.DependsOn {
			if dependency == check.ID || !checkSet[dependency] {
				return planInvalid("unknown or self check dependency")
			}
		}
	}
	visiting, visited := map[string]bool{}, map[string]bool{}
	dependencies := map[string][]string{}
	for _, check := range checks {
		dependencies[check.ID] = check.DependsOn
	}
	var visit func(string) bool
	visit = func(id string) bool {
		if visiting[id] {
			return false
		}
		if visited[id] {
			return true
		}
		visiting[id] = true
		for _, dependency := range dependencies[id] {
			if !visit(dependency) {
				return false
			}
		}
		visiting[id] = false
		visited[id] = true
		return true
	}
	for id := range checkSet {
		if !visit(id) {
			return planInvalid("cyclic check DAG")
		}
	}
	return nil
}

func planWorkingDirectory(mountPath, commandPath string) (string, error) {
	commandPath = strings.TrimSpace(commandPath)
	if commandPath == "." {
		return mountPath, nil
	}
	return repository.NormalizePath(mountPath + "/" + commandPath)
}

func profileCheckTimeout(profile VerificationProfileDocument) int {
	if value, ok := profile.Limits["checkTimeoutSeconds"].(float64); ok && value >= 1 && value <= 7200 {
		return int(value)
	}
	if value, ok := profile.Limits["checkTimeoutSeconds"].(json.Number); ok {
		parsed, err := value.Int64()
		if err == nil && parsed >= 1 && parsed <= 7200 {
			return int(parsed)
		}
	}
	return 900
}

func cloneJSONObject(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return map[string]any{}
	}
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.UseNumber()
	result := map[string]any{}
	if err := decoder.Decode(&result); err != nil {
		return map[string]any{}
	}
	return result
}

func planInvalid(detail string) error {
	return fmt.Errorf("%w: %s", ErrInvalidPlan, detail)
}
